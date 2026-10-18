# Set up production backups

This walkthrough sets up what a production instance should have: encrypted
backups offsite on a schedule, a copy in a second place, a weekly restore
drill, regular store checks, and alerts. It uses an instance `my-odoo` in the
namespace `shop`, an S3 bucket as the first store and an SFTP server as the
second; [Set up a backup store](backup-stores.md) has the other kinds.

## 1. Create the encryption key, and keep a copy

Every store encrypts its repositories with a key from a Secret. Generate a
long random one:

```sh
openssl rand -base64 32 > backup-key.txt
kubectl -n shop create secret generic backup-key --from-file=password=backup-key.txt
```

!!! danger "Without the key, no backup can be read"
    Store `backup-key.txt` outside the cluster now, in a password manager or
    a vault. If the cluster is lost, it is the only way to read the backups:
    see [Recover onto a new cluster](disaster-recovery.md). Then delete the
    local file.

Both stores here use the same key; each store may also have its own.

## 2. Create the stores

The S3 credentials, under the two keys the operator reads:

```sh
kubectl -n shop create secret generic s3-credentials \
  --from-literal=AWS_ACCESS_KEY_ID=… \
  --from-literal=AWS_SECRET_ACCESS_KEY=…
```

The SFTP key and the server's host key ([SFTP keys](backup-stores.md#sftp-keys)
has the details):

```sh
ssh-keyscan backup.example.com > known_hosts     # check it against the server's key
kubectl -n shop create secret generic sftp-key \
  --from-file=id_ed25519=$HOME/.ssh/backup_key --from-file=known_hosts
```

```yaml
# stores.yaml
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackupStore
metadata:
  name: offsite
  namespace: shop
spec:
  s3:
    endpoint: https://s3.example.com      # leave out for AWS
    region: eu-central-1
    bucket: odoo-backups
    path: production
    credentialsSecret: {name: s3-credentials}
  encryptionKeySecret: {name: backup-key, key: password}
  check:
    schedule: "0 4 * * 0"                 # Sundays: verify the repositories
    readDataParts: 7                      # read a seventh of the data each time
  prune:
    schedule: "0 2 * * 6"                 # Saturdays: free the space of old backups
  timeZone: Europe/Berlin
---
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackupStore
metadata:
  name: cold
  namespace: shop
spec:
  sftp:
    host: backup.example.com
    user: odoo
    path: backups
    sshSecret: {name: sftp-key}
  encryptionKeySecret: {name: backup-key, key: password}
  prune:
    schedule: "0 3 * * 6"
  timeZone: Europe/Berlin
```

```sh
kubectl apply -f stores.yaml
kubectl -n shop get odoobackupstore
```

Wait until both show `READY true`. A store that is not ready says why in its
`REASON` column and in `kubectl -n shop describe odoobackupstore <store>`.

## 3. Schedule backups on the instance

```yaml
# in the OdooDeployment my-odoo
spec:
  backup:
    stores: [offsite, cold]     # backups go to offsite, and are copied to cold
    schedule: "0 3 * * *"       # every night at 03:00
    timeZone: Europe/Berlin
    beforeUpgrade: Required     # back up before every upgrade, and don't upgrade if that fails
    retention:                  # keep 7 daily, 4 weekly and 12 monthly backups
      daily: 7
      weekly: 4
      monthly: 12
      manual: 10                # and the 10 newest manual ones
      preUpgrade: 5             # and the 5 newest taken before upgrades (the default)
    drill:
      schedule: "0 5 * * 0"     # Sundays: prove the newest backup restores
```

[Take and schedule backups](backups.md) explains the retention rules, what is
never deleted, and copies; [Restore drills](drills.md) what a drill checks.

## 4. Take a first backup now, and check it

Don't wait for the schedule:

```yaml
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackup
metadata:
  name: my-odoo-first
  namespace: shop
spec:
  odooDeployment: my-odoo
  keep: true                    # never removed by retention
```

```sh
kubectl -n shop get odoobackup my-odoo-first -w
kubectl -n shop get odoobackup my-odoo-first -o jsonpath='{.status.copies}'
```

It should end `Completed`, with a copy in `cold` in `status.copies`.
`PartiallyCompleted` means the backup is in `offsite` but the copy failed;
`CompletedWithWarnings` lists what is missing in `status.message` and
`status.missingFileNames`. See [Troubleshooting](../operations/troubleshooting.md).

## 5. Watch it

Scrape the operator's metrics and install the example alerts, as [Monitoring
and alerts](monitoring.md) describes. They fire when a backup is stale or
failed, a copy failed, a drill failed or is overdue, or a store is not ready
or fails its check or prune. The [Grafana dashboard](grafana-dashboard.md)
shows the same on one screen.

## 6. Try a restore

A backup you have never restored is a hope. The drill restores the newest
backup into scratch space every week without touching the instance. Once, in
a maintenance window, also do a real restore and its undo, following [Restore
and undo](restore.md), so the steps are known before they are needed.

## Checklist

- [ ] The encryption key is stored outside the cluster.
- [ ] Both stores are `Ready`.
- [ ] A first backup is `Completed`, with its copy.
- [ ] Scheduled backups appear (`kubectl -n shop get odoobackup`).
- [ ] `status.backup.lastDrill.succeeded` is `true` after the first drill.
- [ ] The stores' `status.lastCheck` and `status.lastPrune` pass.
- [ ] The alerts are loaded, and reach someone.
