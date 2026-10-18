# Recover onto a new cluster

When the cluster, or the instance's namespace, is gone, its backups are
still in the store. This page restores an instance from them onto a new
cluster. You need:

- the **encryption key** of the store, kept outside the cluster (see [Set up
  production backups](production-backups.md#1-create-the-encryption-key-and-keep-a-copy));
- access to the store (its credentials, SSH key or volume);
- the instance's old **namespace** and **name**;
- [restic](https://restic.net/) on a workstation, to list the backups.

!!! warning "Rehearse it"
    This procedure follows how the operator finds and restores backups, but
    the restore onto a never-initialised instance it relies on is not covered
    by the operator's end-to-end tests yet. Try it on a scratch cluster
    before you depend on it.

## Why the name matters

An instance's repository in a store is at `<path>/<namespace>/<name>/`: the
store's path, then the OdooDeployment's namespace and `metadata.name`. The
operator finds the backups only if the new instance has the **same namespace
and name** and uses a store with the same location (bucket and path, server
and path, export, or volume and `subPath`) and the same key. The store's own
name may differ. Restoring into another namespace or name is not supported.

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

## 3. Create the restore first

Create the OdooRestore **before** the instance. It waits, `Pending`, until
the instance exists, and then runs before the instance's init Job would
install anything:

```yaml
apiVersion: odoo.abugharbia.com/v1
kind: OdooRestore
metadata:
  name: recover
  namespace: shop
spec:
  odooDeployment: my-odoo
  snapshotID: <snapshot-id>      # a unique prefix is enough
```

## 4. Create the instance

Create the `OdooDeployment` as it was, with these points checked:

- `metadata.name: my-odoo` and namespace `shop`, as before;
- `spec.database.createPolicy: IfNotExists` (the default): the operator
  creates an empty database for the restore to replace. With `Never` and no
  database the instance stops at `DatabaseMissing` and the restore never
  runs;
- `spec.backup.stores` lists the recreated store **first**: a restore by
  snapshot ID reads only the first store;
- `spec.image` and `spec.upgrade.token` set to the backup's
  `operator.image` and `operator.upgradeToken`, so no upgrade follows the
  restore;
- `spec.modules` limited to modules the backup has; any others are installed
  after the restore;
- for now, no `spec.backup.schedule`, and `spec.backup.retention.keepAll: true`
  (see step 6).

The operator creates the filestore volume and an empty database, runs the
restore, and starts Odoo on the backup's image:

```sh
kubectl -n shop get odoorestore recover -w
kubectl -n shop get odoodeployment my-odoo
```

The restore ends `Completed`; any warnings are in `status.warnings`. The empty database it replaced is kept as
`<db>_prev_<suffix>` for `keepPrevious`, then deleted.

## 5. Check Odoo

Log in, check recent records and attachments, and only then point users and
the Ingress at the new instance.

## 6. Turn backups back on

Set the schedule again. Before you remove `keepAll`, know that retention on
the new instance applies to the old snapshots too: they carry the same host
and tags, so the daily, weekly and monthly rules thin them like new ones.
Old snapshots cannot be pinned one by one, so keep `keepAll: true` until you
no longer need them.

## Restoring from a copy

If the first store is lost too, restore from a copy: list the copy's store
first in `spec.backup.stores` and use the snapshot ID **in that store**
(`restic snapshots` against the copy's repository; a copy has its own ID).
