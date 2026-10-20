# Glossary

The words these pages use, and what each one means here.

**Instance**
:   One Odoo installation: an `OdooDeployment` and everything it owns (its
    database, filestore volume, Deployment, Services and Jobs). Its
    `metadata.name`, written `<name>` in these pages, names all of them:
    `<name>-http`, `<name>-init`, `<name>-admin-password` (unless
    `spec.config.adminPasswordSecretName` names another Secret).
    `spec.name` is required but not used.

**Store**
:   An `OdooBackupStore`: a place backups go (an S3 bucket, an SFTP server,
    an NFS export or a volume) and the key they are encrypted with. See [Set
    up a backup store](../guides/backup-stores.md).

**Repository**
:   The [restic](https://restic.net/) repository of one instance inside a
    store, at `<path>/<namespace>/<name>/`. A store holds one repository per
    instance that uses it.

**Backup**
:   An `OdooBackup`, and the restic **snapshot** it took: `dump.sql`,
    `manifest.json` and the filestore at one moment. `status.snapshotID`
    links the two. Deleting an OdooBackup leaves its snapshot in the store;
    [retention](../guides/backups.md) forgets snapshots and deletes their
    OdooBackups.

**Scheduled, manual and pre-upgrade backups**
:   A scheduled backup is one the operator creates from
    `spec.backup.schedule`, named `<name>-<yyyymmdd-hhmm>`; a manual backup is
    one you create; a pre-upgrade backup is one the operator takes before an
    upgrade when `spec.backup.beforeUpgrade` asks for it, named
    `<name>-pre-upgrade-<yyyymmdd-hhmmss>`. Retention thins scheduled backups
    by its rules, keeps the newest `retention.preUpgrade` pre-upgrade backups
    (default 5), and keeps manual backups unless you set `retention.manual`.

**Read-only store**
:   A store with `readOnly: true`: the operator only reads backups from it
    (restores and bootstraps with `from`), never writes, locks, checks or
    prunes it. See [Read-only stores](../guides/backup-stores.md#read-only-stores).

**Zip store**
:   A store with `format: zip`: it takes copies of backups as Odoo zips,
    which any Odoo loads, instead of restic snapshots. See [Zip
    stores](../guides/backup-stores.md#zip-stores).

**Backup file**
:   A backup made by Odoo's database manager rather than by the operator: a
    `.zip` (`dump.sql`, `manifest.json`, `filestore/`) or a `.dump`
    (`pg_dump` custom format, no filestore). A restore or bootstrap reads it
    with `from.path`.

**Bootstrap**
:   Making a new instance's first database from a backup
    (`spec.bootstrap.fromBackup`) instead of initializing it: the operator
    creates the OdooRestore `<name>-bootstrap` in place of the init Job. See
    [Start an instance from a backup](../guides/start-from-a-backup.md).

**Neutralize**
:   Odoo's `odoo neutralize`, which a restore runs on the loaded database
    when `neutralize` is set: outgoing mail, scheduled actions, payment
    providers and other integrations are turned off, and the database gets a
    new `database.uuid`. For copies of production.

**Upgrade (of a restore)**
:   `upgrade` on an OdooRestore or `spec.bootstrap.fromBackup`: the instance's
    Odoo runs `-u` on the loaded database before the swap, so a backup made by
    older code runs on `spec.image`'s modules. See
    [Upgrade the restored data](../guides/restore.md#upgrade-the-restored-data).

**Copy**
:   The same snapshot copied (`restic copy`) to every store after the first in
    `spec.backup.stores`; in a zip store, the snapshot written as an Odoo zip.

**Prune**
:   Freeing the space of forgotten snapshots in a store (`restic prune`).
    Retention only forgets; a store's [prune](../guides/store-check-prune.md)
    reclaims the space.

**Check**
:   Verifying a store's repositories (`restic check`), reading a part of the
    data each time.

**Probe**
:   The short Job that writes, reads and deletes a file in a store to decide
    whether it is `Ready`.

**Restore**
:   An `OdooRestore`: replacing an instance's database and filestore with a
    backup's, in place. See [Restore and undo](../guides/restore.md).

**Suffix**
:   The UTC time a restore started, as `20261018T143000`. It names the data a
    restore creates beside the live data: `<db>_restore_<suffix>` while it
    loads, `<db>_prev_<suffix>` for the replaced data afterwards. A drill's
    scratch database is `<db>_drill_<suffix>`. The OdooRestore records it in
    `status.suffix`.

**Previous data / keptUntil**
:   The database and filestore a restore replaced, kept as
    `<db>_prev_<suffix>` until `status.previous.keptUntil`
    (`keepPrevious`, 72 hours by default) so the restore can be undone.

**Undo**
:   An OdooRestore with `spec.undo`, which swaps the previous data back.

**Drill**
:   A scheduled test restore of the newest backup into scratch space, beside
    the instance, which keeps serving. It proves the backup can be restored.
    See [Restore drills](../guides/drills.md).

**Online / offline backup**
:   Online (the default) backs up while Odoo keeps serving, from one
    consistent database snapshot. Offline stops Odoo for the backup.

**Init Job / upgrade Job**
:   The Jobs that install modules on a new database (`<name>-init`) and
    update them on a new image or token (`<name>-upgrade-<hash>`).

**Adopted database**
:   A database that existed before the instance pointed at it
    (`status.database.provisionedBy: external`). The operator never drops an
    adopted database. See [Databases](../concepts/databases.md).

**Manager**
:   The operator's own process, the `odoo-operator-controller-manager`
    Deployment.
