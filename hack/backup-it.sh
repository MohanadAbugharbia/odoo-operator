#!/usr/bin/env bash
# Runs the backup agent's real-service tests inside the backup image, against
# a PostgreSQL server, an SFTP server and an S3 server started here. CI runs it
# once per PostgreSQL major; it works the same on a workstation with Docker:
#
#   PG_MAJOR=17 hack/backup-it.sh
#
# The image is built from Dockerfile.backup, so the tests also prove that the
# image carries a working client for every server major, restic and rclone.
set -euo pipefail

PG_MAJOR="${PG_MAJOR:-16}"
IMAGE="${IMAGE:-odoo-operator-backup:it}"
NET="backup-it"
WORK="$(mktemp -d)"
trap 'docker rm -f backup-it-pg backup-it-sftp backup-it-s3 >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; rm -rf "$WORK"' EXIT

cd "$(dirname "$0")/.."
mkdir -p bin
CGO_ENABLED=0 go build -o bin/odoo-backup ./cmd/odoo-backup
CGO_ENABLED=0 go test -c -cover -coverpkg=./internal/backup/... -o "$WORK/agent.test" ./internal/backup/agent/
if [ -z "${SKIP_IMAGE_BUILD:-}" ]; then
	docker build -q -t "$IMAGE" --build-arg TARGETPLATFORM=bin -f Dockerfile.backup . >/dev/null
fi
mkdir -p cover && chmod 0777 cover

docker network create "$NET" >/dev/null
docker run -d --name backup-it-pg --network "$NET" -e POSTGRES_PASSWORD=postgres "postgres:$PG_MAJOR" >/dev/null

# SFTP: a user with key authentication; the host key goes to known_hosts.
mkdir -p "$WORK/ssh"
# The key is made, and made readable for the unprivileged test user, inside
# the container: its files belong to root, which a CI runner is not.
docker run --rm -v "$WORK/ssh:/k" --entrypoint sh atmoz/sftp:alpine -c \
	'ssh-keygen -q -t ed25519 -N "" -f /k/id_ed25519 && chmod 0644 /k/id_ed25519'
docker run -d --name backup-it-sftp --network "$NET" \
	-v "$WORK/ssh/id_ed25519.pub:/home/backup/.ssh/keys/id.pub:ro" atmoz/sftp:alpine backup::1001:1001:upload >/dev/null

# S3: SeaweedFS with one identity, so wrong credentials are refused.
cat >"$WORK/s3.json" <<'JSON'
{"identities":[{"name":"backup","credentials":[{"accessKey":"backupkey","secretKey":"backupsecret123"}],"actions":["Admin","Read","Write","List","Tagging"]}]}
JSON
docker run -d --name backup-it-s3 --network "$NET" -v "$WORK/s3.json:/etc/s3.json:ro" \
	chrislusf/seaweedfs:4.48 server -s3 -s3.config=/etc/s3.json -dir=/data -master.volumeSizeLimitMB=64 >/dev/null

for i in $(seq 1 60); do
	if docker exec backup-it-pg pg_isready -U postgres >/dev/null 2>&1 &&
		docker exec backup-it-sftp test -f /etc/ssh/ssh_host_ed25519_key.pub 2>/dev/null &&
		docker run --rm --network "$NET" busybox:1.37 nc -z -w 2 backup-it-s3 8333 2>/dev/null; then
		break
	fi
	[ "$i" = 60 ] && { echo "services did not start" >&2; exit 1; }
	sleep 2
done
docker exec backup-it-sftp cat /etc/ssh/ssh_host_ed25519_key.pub |
	awk '{print "backup-it-sftp " $1 " " $2}' >"$WORK/ssh/known_hosts"
chmod 0644 "$WORK/ssh/known_hosts"
cp "$WORK/agent.test" "$WORK/ssh/"

# The tests run as an unprivileged user, as the Jobs do.
docker run --rm --network "$NET" --user 65532:65532 -v "$WORK/ssh:/it:ro" -v "$PWD/cover:/cover" \
	-e TEST_BACKUP_PG_HOST=backup-it-pg -e TEST_BACKUP_PG_PASSWORD=postgres \
	-e TEST_SFTP_HOST=backup-it-sftp -e TEST_SFTP_USER=backup -e TEST_SFTP_PATH=upload \
	-e TEST_SFTP_KEY_FILE=/it/id_ed25519 -e TEST_SFTP_KNOWN_HOSTS=/it/known_hosts \
	-e TEST_S3_ENDPOINT=http://backup-it-s3:8333 -e TEST_S3_BUCKET=odoo-it \
	-e TEST_S3_ACCESS_KEY_ID=backupkey -e TEST_S3_SECRET_ACCESS_KEY=backupsecret123 \
	-w /tmp --entrypoint /it/agent.test "$IMAGE" \
	-test.v -test.count=1 -test.timeout=45m -test.coverprofile="/cover/backup-it-pg$PG_MAJOR.out"
