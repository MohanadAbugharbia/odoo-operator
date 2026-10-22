#!/usr/bin/env bash
# End-to-end backup and restore in a kind cluster: the operator and the
# backup image built from this checkout, a real Odoo, PostgreSQL, an S3
# server and a PVC store. It backs up, changes the data, restores, undoes the
# restore and checks the database and filestore at every step; then runs
# scheduled backups with retention and a copy, a restore drill through the
# real Odoo image, a store check and prune, backups copied as Odoo zips that
# Odoo's own CLI loads, a backup before an upgrade to a store that cannot be
# reached (Odoo keeps serving), and a failed upgrade undone with the backup
# taken before it. Last, a second instance starts from Odoo's own backup file
# (bootstrap, neutralized and upgraded) and restores the first instance's repository from
# a readOnly store.
#
#   kind create cluster && hack/backup-e2e.sh
set -Eeuo pipefail

cd "$(dirname "$0")/.."
NS=odoo-e2e
OD=e2e-backup
DB=e2e_backup
IMG=example.com/odoo-operator:e2e
BACKUP_IMG=example.com/odoo-operator-backup:e2e
KIND_CLUSTER="${KIND_CLUSTER:-kind}"

log() { printf '\n=== %s\n' "$*"; }
k() { kubectl -n "$NS" "$@"; }
psql_q() { k exec deploy/e2e-postgres -- psql -U odoo -d "${2:-$DB}" -tAc "$1"; }
fail() {
	trap - ERR
	echo "FAILED: $*" >&2
	k get odoodeployment,odoobackup,odoorestore,odoobackupstore,job,pod -o wide >&2 || true
	k describe odoodeployment "$OD" >&2 || true
	for j in $(k get jobs -o name 2>/dev/null); do echo "--- logs $j" >&2; k logs "$j" --tail=50 >&2 || true; done
	kubectl -n odoo-operator-system get pods -o wide >&2 || true
	kubectl -n odoo-operator-system describe pods >&2 || true
	kubectl -n odoo-operator-system logs deploy/odoo-operator-controller-manager --all-containers --tail=200 >&2 || true
	kubectl -n odoo-operator-system logs deploy/odoo-operator-controller-manager --all-containers --previous --tail=100 >&2 || true
	exit 1
}
trap 'fail "command failed at line $LINENO"' ERR
# wait_for <resource> <jsonpath> <value> [timeout seconds]
wait_for() {
	local deadline=$((SECONDS + ${4:-300})) got
	while :; do
		got="$(k get $1 -o "jsonpath={$2}" 2>/dev/null || true)"
		[ "$got" = "$3" ] && return 0
		case "$got" in Failed) fail "$1 $2 is Failed: $(k get $1 -o 'jsonpath={.status.message}')" ;; esac
		[ $SECONDS -ge $deadline ] && fail "$1 $2 is '$got', want '$3'"
		sleep 5
	done
}
odoo_exec() { k exec deploy/"$OD" -c odoo -- "$@"; }
odoo_pod_uid() {
	k get pod -o jsonpath='{.items[0].metadata.uid}' -l "$(k get deploy "$OD" -o jsonpath='{.spec.selector.matchLabels}' |
		jq -r 'to_entries | map("\(.key)=\(.value)") | join(",")')"
}
data_dir() { k get odoodeployment "$OD" -o jsonpath='{.spec.config.dataDir}'; }

log "build and load the images"
make docker-build IMG="$IMG"
make docker-build-backup BACKUP_IMG="$BACKUP_IMG"
kind load docker-image --name "$KIND_CLUSTER" "$IMG" "$BACKUP_IMG"

log "deploy the operator"
make deploy IMG="$IMG"
kubectl -n odoo-operator-system rollout status deploy/odoo-operator-controller-manager --timeout=3m

log "PostgreSQL, the stores and the instance"
kubectl create ns "$NS" --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f test/e2e/testdata/postgres.yaml
k rollout status deploy/e2e-postgres --timeout=3m
kubectl apply -f test/e2e/testdata/backup/stores.yaml
k rollout status deploy/e2e-s3 --timeout=3m
wait_for odoobackupstore/s3 .status.ready true 420
wait_for odoobackupstore/pvc .status.ready true 420
kubectl apply -f test/e2e/testdata/backup/odoodeployment.yaml
wait_for odoodeployment/"$OD" .status.phase Running 900
k rollout status deploy/"$OD" --timeout=5m

# The data the restore must bring back: a row and an attachment file.
FILESTORE="$(data_dir)/filestore/$DB"
put_state() { # <word>
	psql_q "CREATE TABLE IF NOT EXISTS e2e_marker (word text); DELETE FROM e2e_marker; INSERT INTO e2e_marker VALUES ('$1');"
	local sha
	sha="$(printf '%s' "$1" | sha1sum | cut -c1-40)"
	odoo_exec sh -c "mkdir -p '$FILESTORE/${sha:0:2}' && printf '%s' '$1' > '$FILESTORE/${sha:0:2}/$sha'"
	psql_q "DELETE FROM ir_attachment WHERE name = 'e2e'; INSERT INTO ir_attachment (name, type, store_fname, res_model) VALUES ('e2e', 'binary', '${sha:0:2}/$sha', 'res.partner');"
}
check_state() { # <word> <what>
	local word sha
	word="$(psql_q 'SELECT word FROM e2e_marker')"
	[ "$word" = "$1" ] || fail "$2: e2e_marker holds '$word', want '$1'"
	sha="$(printf '%s' "$1" | sha1sum | cut -c1-40)"
	k rollout status deploy/"$OD" --timeout=5m
	[ "$(odoo_exec cat "$FILESTORE/${sha:0:2}/$sha")" = "$1" ] || fail "$2: attachment file for '$1' is missing"
	psql_q "SELECT 1" postgres >/dev/null
}

backup_restore_cycle() { # <store>
	local store=$1 b="e2e-$1" r="e2e-$1-restore" u="e2e-$1-undo"
	log "[$store] point the instance at the store"
	k patch odoodeployment "$OD" --type=merge -p "{\"spec\":{\"backup\":{\"stores\":[\"$store\"]}}}"

	log "[$store] back up"
	put_state "before-$store"
	cat <<YAML | kubectl apply -f -
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackup
metadata: { name: $b, namespace: $NS }
spec: { odooDeployment: $OD }
YAML
	wait_for odoobackup/"$b" .status.phase Completed 900
	[ -n "$(k get odoobackup "$b" -o jsonpath='{.status.snapshotID}')" ] || fail "no snapshot recorded"

	log "[$store] change the data, then restore"
	put_state "after-$store"
	cat <<YAML | kubectl apply -f -
apiVersion: odoo.abugharbia.com/v1
kind: OdooRestore
metadata: { name: $r, namespace: $NS }
spec: { odooDeployment: $OD, backup: $b }
YAML
	wait_for odoorestore/"$r" .status.phase Completed 900
	wait_for odoodeployment/"$OD" .status.phase Running 600
	check_state "before-$store" "after the restore"
	local suffix prev
	suffix="$(k get odoorestore "$r" -o jsonpath='{.status.suffix}')"
	prev="$(k get odoorestore "$r" -o jsonpath='{.status.previous.database}')"
	[ "$(psql_q "SELECT count(*) FROM pg_database WHERE datname = '$prev'" postgres)" = 1 ] || fail "replaced database $prev is not kept"
	[ "$(psql_q "SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = '$DB'" postgres)" = "odoo-operator:$NS/$OD" ] ||
		fail "the restored database lost the operator's owner tag"

	log "[$store] undo"
	cat <<YAML | kubectl apply -f -
apiVersion: odoo.abugharbia.com/v1
kind: OdooRestore
metadata: { name: $u, namespace: $NS }
spec: { odooDeployment: $OD, undo: $r }
YAML
	wait_for odoorestore/"$u" .status.phase Completed 900
	wait_for odoodeployment/"$OD" .status.phase Running 600
	check_state "after-$store" "after the undo"
	wait_for odoorestore/"$r" .status.previous.state Undone 60

	log "[$store] deleting the restores deletes the data they kept"
	k delete odoorestore "$u" "$r" --wait=true --timeout=10m
	local left
	left="$(psql_q "SELECT string_agg(datname, ',') FROM pg_database WHERE datname LIKE '${DB}\_%'" postgres)"
	[ -z "$left" ] || fail "databases left after the cleanup: $left"
	check_state "after-$store" "after the cleanup"
	echo "[$store] ok (suffix $suffix)"
}

backup_restore_cycle s3
backup_restore_cycle pvc

scheduled_count() { k get odoobackup -l odoo.abugharbia.com/backup-kind=scheduled,odoo.abugharbia.com/odoodeployment="$OD" -o name | wc -l; }

log "scheduled backups every minute, keeping the last 2, copied to the PVC store"
k patch odoodeployment "$OD" --type=merge -p '{"spec":{"backup":{"stores":["s3","pvc"],"schedule":"* * * * *",
  "retention":{"last":2,"daily":0,"weekly":0,"monthly":0}}}}'
deadline=$((SECONDS + 900))
first=""
while :; do
	[ $SECONDS -ge $deadline ] && fail "retention never deleted the first scheduled backup ($first)"
	names="$(k get odoobackup -l odoo.abugharbia.com/backup-kind=scheduled -o jsonpath='{range .items[*]}{.metadata.name}={.status.phase} {end}')"
	case "$names" in *=Failed*) fail "a scheduled backup failed: $names" ;; esac
	[ -z "$first" ] && first="$(printf '%s\n' $names | sort | head -1 | cut -d= -f1)"
	if [ -n "$first" ] && ! k get odoobackup "$first" >/dev/null 2>&1; then break; fi
	sleep 10
done
k patch odoodeployment "$OD" --type=merge -p '{"spec":{"backup":{"schedule":""}}}'
[ "$(scheduled_count)" -le 3 ] || fail "retention keeps $(scheduled_count) scheduled backups, want at most 2 (+1 running)"
newest="$(k get odoobackup -l odoo.abugharbia.com/backup-kind=scheduled -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort | tail -1)"
wait_for odoobackup/"$newest" .status.phase Completed 600
[ "$(k get odoobackup "$newest" -o jsonpath='{.status.copies[0].store}')" = pvc ] || fail "$newest was not copied to pvc"
[ -n "$(k get odoobackup "$newest" -o jsonpath='{.status.copies[0].snapshotID}')" ] || fail "$newest has no copy snapshot"
for b in e2e-s3 e2e-pvc; do k get odoobackup "$b" >/dev/null || fail "retention deleted manual backup $b"; done

log "a restore drill through the real Odoo image"
k patch odoodeployment "$OD" --type=merge -p '{"spec":{"backup":{"drill":{"schedule":"* * * * *"}}}}'
deadline=$((SECONDS + 900))
while :; do
	[ $SECONDS -ge $deadline ] && fail "no restore drill finished"
	done_at="$(k get odoodeployment "$OD" -o jsonpath='{.status.backup.lastDrill.completedAt}')"
	[ -n "$done_at" ] && break
	sleep 10
done
k patch odoodeployment "$OD" --type=merge -p '{"spec":{"backup":{"drill":{"schedule":""}}}}'
# A drill that came due meanwhile finishes before the scratch databases are counted.
while [ "$(k get odoodeployment "$OD" -o jsonpath='{.status.currentOperation.kind}')" = drill ]; do
	[ $SECONDS -ge $deadline ] && fail "a drill is still running"
	sleep 5
done
[ "$(k get odoodeployment "$OD" -o jsonpath='{.status.backup.lastDrill.succeeded}')" = true ] ||
	fail "the drill failed: $(k get odoodeployment "$OD" -o jsonpath='{.status.backup.lastDrill.message}')"
k get odoodeployment "$OD" -o jsonpath='{.status.backup.lastDrill.message}{"\n"}'
left="$(psql_q "SELECT string_agg(datname, ',') FROM pg_database WHERE datname LIKE '${DB}\_drill\_%'" postgres)"
[ -z "$left" ] || fail "scratch databases left after the drill: $left"
check_state "after-pvc" "after the drill"

log "check and prune the S3 store"
kubectl -n "$NS" patch odoobackupstore s3 --type=merge -p '{"spec":{"check":{"schedule":"* * * * *","readDataParts":1},"prune":{"schedule":"* * * * *"}}}'
wait_for odoobackupstore/s3 .status.lastPrune.result Passed 900
wait_for odoobackupstore/s3 .status.lastCheck.result Passed 900
kubectl -n "$NS" patch odoobackupstore s3 --type=merge -p '{"spec":{"check":{"schedule":""},"prune":{"schedule":""}}}'

log "scheduled backups copied as Odoo zips into a zip store, which Odoo's own CLI loads"
cat <<YAML | kubectl apply -f -
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackupStore
metadata: { name: zips, namespace: $NS }
spec:
  pvc: { claimName: e2e-backups, subPath: zips }
  format: zip
YAML
wait_for odoobackupstore/zips .status.ready true 420
ls_zips() { # the instance's folder in the zip store, one name per line
	k delete pod e2e-ls-zips --ignore-not-found --wait=true >/dev/null
	cat <<YAML | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata: { name: e2e-ls-zips, namespace: $NS }
spec:
  restartPolicy: Never
  containers:
    - name: ls
      image: busybox:1.37
      command: ["sh", "-c", "ls -A /pvc/zips/$NS/$OD 2>/dev/null; true"]
      volumeMounts: [{ name: pvc, mountPath: /pvc, readOnly: true }]
  volumes: [{ name: pvc, persistentVolumeClaim: { claimName: e2e-backups } }]
YAML
	wait_for pod/e2e-ls-zips .status.phase Succeeded 300 >/dev/null
	k logs e2e-ls-zips
}
# A backup listed a moment ago may be gone: retention keeps the last one.
zip_of() { k get odoobackup "$1" -o jsonpath='{.status.copies[?(@.store=="zips")].file}' 2>/dev/null || true; }
k patch odoodeployment "$OD" --type=merge -p '{"spec":{"backup":{"stores":["s3","zips"],"schedule":"* * * * *",
  "retention":{"last":1,"daily":0,"weekly":0,"monthly":0}}}}'
deadline=$((SECONDS + 900))
first="" first_zip=""
while :; do
	[ $SECONDS -ge $deadline ] && fail "retention never deleted the first backup copied as a zip ($first)"
	if [ -z "$first" ]; then
		for b in $(k get odoobackup -l odoo.abugharbia.com/backup-kind=scheduled -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort); do
			z="$(zip_of "$b")"
			if [ -n "$z" ]; then first="$b" first_zip="$z"; break; fi
		done
	elif ! k get odoobackup "$first" >/dev/null 2>&1; then
		break
	fi
	sleep 10
done
k patch odoodeployment "$OD" --type=merge -p '{"spec":{"backup":{"schedule":""}}}'
while [ "$(k get odoodeployment "$OD" -o jsonpath='{.status.currentOperation.kind}')" = backup ]; do
	[ $SECONDS -ge $deadline ] && fail "a backup is still running"
	sleep 5
done
newest="$(k get odoobackup -l odoo.abugharbia.com/backup-kind=scheduled -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort | tail -1)"
wait_for odoobackup/"$newest" .status.phase Completed 600
zip="$(zip_of "$newest")"
case "$zip" in "$NS/$OD/${DB}_"*.zip) ;; *) fail "$newest records zip '$zip'" ;; esac
zips="$(ls_zips)"
echo "$zips"
printf '%s\n' "$zips" | grep -qx "$(basename "$zip")" || fail "the zip of $newest is not in the store"
! printf '%s\n' "$zips" | grep -qx "$(basename "$first_zip")" || fail "the zip of $first stayed after retention deleted it"
! printf '%s\n' "$zips" | grep -q partial || fail "a partial upload was left in the store"

secret="$(k get odoodeployment "$OD" -o jsonpath='{.status.odooConfigSecretName}')"
sha="$(printf '%s' after-pvc | sha1sum | cut -c1-40)"
cat <<YAML | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata: { name: e2e-odoo-load, namespace: $NS }
spec:
  restartPolicy: Never
  containers:
    - name: odoo
      image: odoo:18
      command: ["sh", "-c", "odoo db -c /opt/odoo/odoo.conf -D /tmp/data load ${DB}_zipload /pvc/zips/$zip &&
        cat /tmp/data/filestore/${DB}_zipload/${sha:0:2}/$sha"]
      volumeMounts:
        - { name: pvc, mountPath: /pvc, readOnly: true }
        - { name: config, mountPath: /opt/odoo, readOnly: true }
  volumes:
    - { name: pvc, persistentVolumeClaim: { claimName: e2e-backups } }
    - { name: config, secret: { secretName: $secret } }
YAML
wait_for pod/e2e-odoo-load .status.phase Succeeded 600
[ "$(k logs e2e-odoo-load | tail -1)" = after-pvc ] || { k logs e2e-odoo-load | tail -40; fail "odoo db load did not bring the attachment file back"; }
[ "$(psql_q 'SELECT word FROM e2e_marker' "${DB}_zipload")" = after-pvc ] || fail "odoo db load did not bring the data back"
psql_q "DROP DATABASE ${DB}_zipload" postgres
k patch odoodeployment "$OD" --type=merge -p '{"spec":{"backup":{"stores":["s3"]}}}'
k delete pod e2e-odoo-load e2e-ls-zips
k delete odoobackupstore zips
k get odoobackupstore s3 -o jsonpath='{.status.lastCheck.message}{"\n"}{.status.lastPrune.message}{"\n"}'

log "a backup before an upgrade to a store that cannot be reached never stops Odoo"
# The same S3 server on a port nothing listens on: the store is not there.
cat <<YAML | kubectl apply -f -
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackupStore
metadata: { name: unreachable, namespace: $NS }
spec:
  s3:
    endpoint: http://e2e-s3.$NS.svc.cluster.local:1
    region: us-east-1
    bucket: odoo-backups
    path: e2e
    forcePathStyle: true
    credentialsSecret: { name: e2e-s3-credentials }
  encryptionKeySecret: { name: e2e-restic, key: password }
YAML
pod_before="$(odoo_pod_uid)"
k patch odoodeployment "$OD" --type=merge -p '{"spec":{"backup":{"stores":["unreachable"],"beforeUpgrade":"Required"},
  "upgrade":{"token":"e2e-unreachable"}}}'
deadline=$((SECONDS + 300))
until pre="$(k get odoodeployment "$OD" -o jsonpath='{.status.preUpgradeBackup.name}')" && [ -n "$pre" ]; do
	[ $SECONDS -ge $deadline ] && fail "no backup was taken before the upgrade"
	sleep 5
done
deadline=$((SECONDS + 600))
until [ "$(k get odoobackup "$pre" -o jsonpath='{.status.phase}')" = Failed ]; do
	[ "$(k get deploy "$OD" -o jsonpath='{.spec.replicas}')" = 1 ] || fail "Odoo was stopped for a backup to a store that cannot be reached"
	[ $SECONDS -ge $deadline ] && fail "$pre did not fail on the store that cannot be reached"
	sleep 5
done
k get odoobackup "$pre" -o jsonpath='{.status.message}' | grep -q "cannot be reached, so Odoo was not stopped" ||
	fail "$pre failed for another reason: $(k get odoobackup "$pre" -o jsonpath='{.status.message}')"
[ -z "$(k get odoobackup "$pre" -o jsonpath='{.status.job}')" ] || fail "a backup Job ran for $pre"
wait_for odoodeployment/"$OD" '.status.conditions[?(@.type=="Degraded")].reason' PreUpgradeBackupFailed 120
[ "$(k get deploy "$OD" -o jsonpath='{.spec.replicas}')" = 1 ] || fail "Odoo is not serving after the failed backup"
pod_after="$(odoo_pod_uid)"
[ "$pod_before" = "$pod_after" ] || fail "the Odoo pod was replaced during the failed backup"
k patch odoodeployment "$OD" --type=merge -p '{"spec":{"backup":{"stores":["s3"],"beforeUpgrade":"Off"},"upgrade":{"token":null}}}'
k delete odoobackup "$pre" --wait=true --timeout=3m
k delete odoobackupstore unreachable --wait=true --timeout=3m
wait_for odoodeployment/"$OD" .status.preUpgradeBackup.name "" 120

log "a backup before an upgrade, the upgrade failing, and the way back"
put_state "before-upgrade"
# The upgrade fails at the database: nothing may change a module's state.
psql_q "CREATE FUNCTION e2e_block_upgrade() RETURNS trigger LANGUAGE plpgsql AS
	\$\$ BEGIN RAISE EXCEPTION 'e2e: upgrades are blocked'; END \$\$;
	CREATE TRIGGER e2e_block_upgrade BEFORE UPDATE ON ir_module_module FOR EACH ROW
	WHEN (NEW.state IS DISTINCT FROM OLD.state) EXECUTE FUNCTION e2e_block_upgrade();"
k patch odoodeployment "$OD" --type=merge -p '{"spec":{"backup":{"stores":["s3"],"beforeUpgrade":"Required"},
  "upgrade":{"modules":["base"],"token":"e2e-blocked"}}}'
deadline=$((SECONDS + 300))
until pre="$(k get odoodeployment "$OD" -o jsonpath='{.status.preUpgradeBackup.name}')" && [ -n "$pre" ]; do
	[ $SECONDS -ge $deadline ] && fail "no backup was taken before the upgrade"
	sleep 5
done
[ "$(k get odoobackup "$pre" -o jsonpath='{.metadata.labels.odoo\.abugharbia\.com/backup-kind}')" = pre-upgrade ] ||
	fail "$pre is not labelled pre-upgrade"
wait_for odoobackup/"$pre" .status.phase Completed 900
[ "$(k get odoobackup "$pre" -o jsonpath='{.status.consistency}')" = Offline ] || fail "$pre ran with Odoo running"
wait_for odoodeployment/"$OD" .status.phase Failed 900
k get odoodeployment "$OD" -o jsonpath='{.status.currentInitJob.name}{"\n"}'

log "undo the upgrade: back to the old token, restore the backup taken before it"
k patch odoodeployment "$OD" --type=merge -p '{"spec":{"upgrade":{"token":null}}}'
cat <<YAML | kubectl apply -f -
apiVersion: odoo.abugharbia.com/v1
kind: OdooRestore
metadata: { name: e2e-undo-upgrade, namespace: $NS }
spec: { odooDeployment: $OD, backup: $pre }
YAML
wait_for odoorestore/e2e-undo-upgrade .status.phase Completed 900
wait_for odoodeployment/"$OD" .status.phase Running 600
check_state "before-upgrade" "after restoring the backup taken before the upgrade"
psql_q "DROP TRIGGER e2e_block_upgrade ON ir_module_module; DROP FUNCTION e2e_block_upgrade();"
[ -z "$(k get odoodeployment "$OD" -o jsonpath='{.status.preUpgradeBackup.name}')" ] ||
	fail "status.preUpgradeBackup is kept with no upgrade pending"
k delete odoorestore e2e-undo-upgrade --wait=true --timeout=10m
k patch odoodeployment "$OD" --type=merge -p '{"spec":{"backup":{"beforeUpgrade":"Off"}}}'

log "a new instance starts from Odoo's own backup file in a readOnly store, neutralized and upgraded"
BOOT=e2e-boot
BOOT_DB=e2e_boot
FIXTURES=internal/backup/agent/testdata/odoo/18.0
FIXTURE_SHA=b1780c43e423d9840c8bbf5f9e8a36580d76b83a
FIXTURE_UUID=27eb0a80-cb43-11f1-ad4f-39ab03fe46e7
boot_psql() { psql_q "$1" "$BOOT_DB"; }
k create configmap e2e-fixture-new --from-file=shop_2026-10-01.zip="$FIXTURES/fixture.zip"
k create configmap e2e-fixture-old --from-file=shop_2026-09-01.zip="$FIXTURES/wrapped.zip"
cat <<YAML | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata: { name: e2e-upload, namespace: $NS }
spec:
  restartPolicy: Never
  containers:
    - name: upload
      image: busybox:1.37
      command: ["sh", "-c", "mkdir -p /pvc/imports/exports && cp /new/* /old/* /pvc/imports/exports/ && ls -l /pvc/imports/exports"]
      volumeMounts:
        - { name: pvc, mountPath: /pvc }
        - { name: new, mountPath: /new }
        - { name: old, mountPath: /old }
  volumes:
    - { name: pvc, persistentVolumeClaim: { claimName: e2e-backups } }
    - { name: new, configMap: { name: e2e-fixture-new } }
    - { name: old, configMap: { name: e2e-fixture-old } }
YAML
wait_for pod/e2e-upload .status.phase Succeeded 300
k logs e2e-upload
cat <<YAML | kubectl apply -f -
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackupStore
metadata: { name: imports, namespace: $NS }
spec:
  pvc: { claimName: e2e-backups, subPath: imports }
  readOnly: true
YAML
wait_for odoobackupstore/imports .status.ready true 420
sed -e "s/name: $OD\$/name: $BOOT/" -e "s/name: $DB\$/name: $BOOT_DB/" -e '/^  backup:/,$d' \
	test/e2e/testdata/backup/odoodeployment.yaml | kubectl apply -f -
k patch odoodeployment "$BOOT" --type=merge -p '{"spec":{"upgrade":{"modules":["base"]},
  "bootstrap":{"fromBackup":{"store":"imports","path":"exports/shop_*.zip","neutralize":true,"upgrade":true}}}}'
wait_for odoorestore/"$BOOT"-bootstrap .status.phase Completed 900
boot_started="$(k get odoorestore "$BOOT"-bootstrap -o jsonpath='{.metadata.creationTimestamp}')"
wait_for odoodeployment/"$BOOT" .status.phase Running 900
k rollout status deploy/"$BOOT" --timeout=5m
[ "$(k get odoorestore "$BOOT"-bootstrap -o jsonpath='{.status.file}')" = exports/shop_2026-10-01.zip ] ||
	fail "the bootstrap did not pick the newest file: $(k get odoorestore "$BOOT"-bootstrap -o jsonpath='{.status.file}')"
[ "$(k get odoodeployment "$BOOT" -o jsonpath='{.status.bootstrap.completed}')" = true ] || fail "status.bootstrap is not completed"
! k get job "$BOOT"-init >/dev/null 2>&1 || fail "an init Job ran over the restored database"
[ "$(boot_psql "SELECT store_fname FROM ir_attachment WHERE id = 14")" = "b1/$FIXTURE_SHA" ] || fail "the backup's attachment row is missing"
[ "$(k exec deploy/"$BOOT" -c odoo -- cat "$(data_dir)/filestore/$BOOT_DB/b1/$FIXTURE_SHA")" = "odoo fixture 18.0" ] ||
	fail "the backup's attachment file is missing"
[ "$(boot_psql "SELECT lower(value) FROM ir_config_parameter WHERE key = 'database.is_neutralized'")" = true ] || fail "the database was not neutralized"
# -u base rewrote the module's record in the loaded database, before the swap.
[ "$(boot_psql "SELECT write_date > '$boot_started'::timestamptz AT TIME ZONE 'UTC' FROM ir_module_module WHERE name = 'base'")" = t ] ||
	fail "the restored database was not upgraded"
k get odoorestore "$BOOT"-bootstrap -o jsonpath='{.status.warnings}' | grep -q 'upgraded (-u base)' || fail "the restore does not say it upgraded"
[ "$(k get odoodeployment "$BOOT" -o jsonpath='{.status.appliedImage}')" = odoo:18 ] || fail "the upgraded data does not count as running spec.image"
! k get job -l odoo.abugharbia.com/job-kind=upgrade,odoo.abugharbia.com/odoodeployment="$BOOT" -o name | grep -q . ||
	fail "an upgrade Job ran after the restore had upgraded the data"
uuid="$(boot_psql "SELECT value FROM ir_config_parameter WHERE key = 'database.uuid'")"
[ -n "$uuid" ] && [ "$uuid" != "$FIXTURE_UUID" ] || fail "the neutralized copy kept database.uuid $uuid"
k exec deploy/"$BOOT" -c odoo -- python3 -c \
	"import urllib.request; r = urllib.request.urlopen('http://localhost:8069/web/login'); assert r.status == 200, r.status" ||
	fail "Odoo does not serve the restored database"

log "restore another instance's repository into it, read-only and by tag, then undo"
cat <<YAML | kubectl apply -f -
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackupStore
metadata: { name: s3-readonly, namespace: $NS }
spec:
  s3:
    endpoint: http://e2e-s3.odoo-e2e.svc.cluster.local:8333
    region: us-east-1
    bucket: odoo-backups
    path: e2e
    forcePathStyle: true
    credentialsSecret: { name: e2e-s3-credentials }
  encryptionKeySecret: { name: e2e-restic, key: password }
  readOnly: true
---
apiVersion: odoo.abugharbia.com/v1
kind: OdooRestore
metadata: { name: e2e-boot-from-repo, namespace: $NS }
spec:
  odooDeployment: $BOOT
  from:
    store: s3-readonly
    path: $NS/$OD
    host: $NS/$OD
    tags: [kind=manual]
  neutralize: true
YAML
wait_for odoobackupstore/s3-readonly .status.ready true 420
wait_for odoorestore/e2e-boot-from-repo .status.phase Completed 900
wait_for odoodeployment/"$BOOT" .status.phase Running 900
[ "$(boot_psql 'SELECT word FROM e2e_marker')" = before-s3 ] || fail "the latest manual backup of $OD was not restored"
[ "$(boot_psql "SELECT lower(value) FROM ir_config_parameter WHERE key = 'database.is_neutralized'")" = true ] || fail "the restore was not neutralized"
[ -n "$(k get odoorestore e2e-boot-from-repo -o jsonpath='{.status.snapshotID}')" ] || fail "the snapshot latest resolved to is not recorded"
cat <<YAML | kubectl apply -f -
apiVersion: odoo.abugharbia.com/v1
kind: OdooRestore
metadata: { name: e2e-boot-undo, namespace: $NS }
spec: { odooDeployment: $BOOT, undo: e2e-boot-from-repo }
YAML
wait_for odoorestore/e2e-boot-undo .status.phase Completed 900
wait_for odoodeployment/"$BOOT" .status.phase Running 600
[ "$(boot_psql "SELECT store_fname FROM ir_attachment WHERE id = 14")" = "b1/$FIXTURE_SHA" ] || fail "the undo did not bring the bootstrapped data back"
k delete odoorestore e2e-boot-undo e2e-boot-from-repo "$BOOT"-bootstrap --wait=true --timeout=10m
k delete odoodeployment "$BOOT" --wait=true --timeout=3m
k delete odoobackupstore imports s3-readonly
k delete pod e2e-upload
k delete configmap e2e-fixture-new e2e-fixture-old

# The restore Jobs and their finished pods stay until their TTL; they must not
# hold up deleting the instance. Its filestore volume is retained, so the
# deletion first runs the cleanup Job of each restore not yet cleaned up.
log "deleting the OdooDeployment drops its database"
k delete odoodeployment "$OD" --wait=true --timeout=10m || fail "the OdooDeployment was not deleted"
[ "$(psql_q "SELECT count(*) FROM pg_database WHERE datname LIKE '${DB}%'" postgres)" = 0 ] ||
	fail "databases of $OD left after deleting it"

log "backup and restore e2e passed"
