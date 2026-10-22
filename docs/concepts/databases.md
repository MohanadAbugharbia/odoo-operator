# Databases

How the operator decides whether it owns an instance's database, and when it may drop it.

- On the first reconcile the operator looks the database up. If it exists it is
  **adopted** (`status.database.provisionedBy: external`). If it is missing and
  `spec.database.createPolicy` is `IfNotExists` (default) the operator creates
  it — `CREATE DATABASE … ENCODING 'unicode' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0`,
  the same shape Odoo uses — and tags it with the comment
  `odoo-operator:<namespace>/<name>` (`provisionedBy: operator`). It also tries
  to add the `unaccent` and `pg_trgm` extensions, and carries on without them.
  An adopted database is never replaced by `spec.bootstrap.fromBackup`: the
  bootstrap only fills a database the operator created, and waits with
  `Degraded` reason `BootstrapFailed` otherwise (see
  [Start from a backup](../guides/start-from-a-backup.md#an-existing-database)).
  Restores and drills create databases (restores also rename them) whatever
  `createPolicy` says, so for those the database user needs `CREATEDB`.
- `spec.database.deletionPolicy: Delete` adds the finalizer
  `odoo.abugharbia.com/database`, unless the database was adopted. Without
  it, the finalizer `odoo.abugharbia.com/operations` is added only while a
  backup, restore, cleanup, drill, init or upgrade Job of the instance runs,
  so that deleting the instance waits for that Job's pods rather than
  killing one half way, and is removed once none runs. Neither ever drops a
  database the operator did not create, or one with `Retain`. On deletion
  the operator stops the pods and
  drops the database **only if** it recorded `provisionedBy: operator` **and**
  the database comment still equals its tag. A CR pointing at a pre-existing
  database never drops it, whatever the policy says. Dropping uses
  `DROP DATABASE … WITH (FORCE)`, which needs PostgreSQL 13 or later. If the
  connection Secret is already gone (for example when the whole namespace is
  deleted) the database is left in place with a `DatabaseNotDropped` event.
  With it go the databases the instance's restores made: the data a restore
  replaced and kept for an undo (`<db>_prev_<suffix>`, under the same tag
  check) and the copy a restore loads into (`<db>_restore_<suffix>`, which
  carries no comment and is dropped by that exact name only, never a
  database carrying another owner's tag). That includes a restore the
  deletion cut short, whose Job may have swapped the data in after the
  restore stopped being followed: it is marked `Failed` and `cleanedUp`.
- An instance being deleted settles its own backups and restores: the
  `OdooBackup` and `OdooRestore` reconcilers leave them alone until the
  instance is gone. The deletion waits for a running backup Job, records its
  snapshot on the `OdooBackup` (or the reason it failed), and only then
  removes the instance; a backup still `Running` once the instance is gone
  is marked `Failed`. The deletion also releases the finalizer of the
  instance's `OdooRestore`s, after it dropped what they made, so restores
  deleted together with their instance (`kubectl delete
  odoorestore,odoodeployment --all`, or an Argo CD or Helm uninstall) take
  their kept databases with them as well.
- `spec.database.name` and the `nameFromSecret` selector are immutable so a
  rename can never orphan or drop the wrong database. Changing the value inside
  the Secret that `nameFromSecret` points at stops the instance instead: phase
  `Failed`, `Degraded` reason `DatabaseNameChanged`.
- `spec.odooFilestore.deletionPolicy` works the same way for the filestore PVC
  (Retain by default). The PVC is created once: changing
  `spec.odooFilestore.size` or `storageClassName` later does not change it.
