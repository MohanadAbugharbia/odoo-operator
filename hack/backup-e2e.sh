#!/usr/bin/env bash
# End-to-end backup and restore in a kind cluster: the operator and the
# backup image built from this checkout, a real Odoo, PostgreSQL, an S3
# server and a PVC store. It backs up, changes the data, restores, undoes the
# restore and checks the database and filestore at every step.
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

log "backup and restore e2e passed"
