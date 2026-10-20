# Recover onto a new cluster

When the cluster, or the instance's namespace, is gone, its backups are
still in the store. This page restores an instance from them onto a new
cluster. You need:

- the **encryption key** of the store, kept outside the cluster (see [Set up
  production backups](production-backups.md#1-create-the-encryption-key-and-keep-a-copy));
- access to the store (its credentials, SSH key or volume);
- the instance's old **namespace** and **name**;
- [restic](https://restic.net/) on a workstation, to list the backups.

!!! tip "Practise it"
    This procedure was rehearsed end to end on a fresh cluster: the instance
    came back with its data and attachments, before its init Job ran.
    Practise it once yourself on a scratch cluster, so the steps are known
    before you need them.

## Where the backups are

An instance's repository in a store is at `<path>/<namespace>/<name>/`: the
store's path, then the OdooDeployment's namespace and `metadata.name`. The
new instance names that repository in `spec.bootstrap.fromBackup`, so it can
have another namespace or name than the old one. Keep them the same anyway
if you can: the new instance then also writes its own backups to that
repository, beside the old ones.

## 1. List the backups

The operator keeps no list of snapshots outside the OdooBackups, which went
with the old cluster, so read the repository with restic. For an S3 store
(`endpoint: https://s3.example.com`, `bucket: odoo-backups`,
`path: production`) and the instance `shop/my-odoo`:

```sh
export AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=…
export RESTIC_PASSWORD_FILE=./backup-key.txt          # the saved encryption key
export RESTIC_REPOSITORY=s3:https://s3.example.com/odoo-backups/production/shop/my-odoo
# add -o s3.region=<region> if the store sets one, -o s3.bucket-lookup=path for forcePathStyle

restic snapshots
```

For AWS itself the repository is
`s3:s3.amazonaws.com/<bucket>/<path>/<namespace>/<name>`; for an SFTP, NFS or
volume store point restic at the same folder on that server or volume. Each
snapshot is tagged with its instance (`deployment=<namespace>/<name>`), the
OdooBackup that took it (`backup=…`) and its kind (`kind=scheduled`,
`manual` or `pre-upgrade`).

Pick one, and read what it was taken with:

```sh
restic dump <snapshot-id> /backup/manifest.json
```

Note `operator.image`, `operator.upgradeToken` and the Odoo `version`: the
new instance should start on the same image.

## 2. Recreate the namespace, Secrets and store

On the new cluster, with the operator installed:

```sh
kubectl create namespace shop
kubectl -n shop create secret generic backup-key --from-file=password=backup-key.txt
kubectl -n shop create secret generic s3-credentials \
  --from-literal=AWS_ACCESS_KEY_ID=… --from-literal=AWS_SECRET_ACCESS_KEY=…
```

Then the `OdooBackupStore`, with the same bucket and `path` as before (the
manifest from [Set up production backups](production-backups.md#2-create-the-stores)),
and wait until it is `Ready`.

## 3. Create the instance

Create the `OdooDeployment` as it was, starting from the backup:

```yaml
spec:
  image: odoo:18.0-20261015        # the backup's operator.image
  upgrade:
    token: ""                      # the backup's operator.upgradeToken
  bootstrap:
    fromBackup:
      store: offsite               # the recreated store
      path: shop/my-odoo           # the old instance's repository
      snapshot: <snapshot-id>      # or leave it out for the newest
  backup:
    stores: [offsite]
    retention: { keepAll: true }   # for now, see step 5
  # …the rest as before
```

Check these points:

- `spec.database.createPolicy: IfNotExists` (the default): the operator
  creates an empty database for the restore to replace. With `Never` and no
  database the instance stops at `DatabaseMissing` and the bootstrap never
  runs;
- `spec.image` and `spec.upgrade.token` set to the backup's
  `operator.image` and `operator.upgradeToken`, so no upgrade follows the
  restore;
- `spec.modules` limited to modules the backup has; any others are installed
  after the restore;
- for now, no `spec.backup.schedule`.

The operator creates the filestore volume and an empty database, then the
OdooRestore `<name>-bootstrap` instead of the init Job, and starts Odoo on
the backup's image once it completed:

```sh
kubectl -n shop get odoorestore my-odoo-bootstrap -w
kubectl -n shop get odoodeployment my-odoo
```

The restore ends `Completed`; any warnings are in `status.warnings`. If it
fails, the instance is `Degraded` (`BootstrapFailed`) and Odoo does not
start; fix the cause and delete the OdooRestore to retry (see
[Start an instance from a backup](start-from-a-backup.md#when-the-bootstrap-fails)).
The empty database it replaced is kept as `<db>_prev_<suffix>` for
`keepPrevious`, then deleted.

## 4. Check Odoo

Log in, check recent records and attachments, and only then point users and
the Ingress at the new instance.

## 5. Turn backups back on

Set the schedule again. Before you remove `keepAll`, know that retention on
the new instance applies to the old snapshots too: they carry the same host
and tags, so the daily, weekly and monthly rules thin them like new ones.
Old snapshots cannot be pinned one by one, so keep `keepAll: true` until you
no longer need them.

## Restoring from a copy

If the first store is lost too, start from a copy: name the copy's store in
`fromBackup.store`, and the snapshot ID **in that store** (`restic snapshots`
against the copy's repository; a copy has its own ID), or no snapshot for
the newest.
