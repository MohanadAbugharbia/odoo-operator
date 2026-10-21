# Databases

How the operator decides whether it owns an instance's database, and when it may drop it.

- On the first reconcile the operator looks the database up. If it exists it is
  **adopted** (`status.database.provisionedBy: external`). If it is missing and
  `spec.database.createPolicy` is `IfNotExists` (default) the operator creates
  it — `CREATE DATABASE … ENCODING 'unicode' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0`,
  the same shape Odoo uses — and tags it with the comment
  `odoo-operator:<namespace>/<name>` (`provisionedBy: operator`). It also tries
  to add the `unaccent` and `pg_trgm` extensions, and carries on without them.
  Restores and drills create databases (restores also rename them) whatever
  `createPolicy` says, so for those the database user needs `CREATEDB`.
- `spec.database.deletionPolicy: Delete` adds the finalizer
  `odoo.abugharbia.com/database`, unless the database was adopted. On deletion the operator stops the pods and
  drops the database **only if** it recorded `provisionedBy: operator` **and**
  the database comment still equals its tag. A CR pointing at a pre-existing
  database never drops it, whatever the policy says. Dropping uses
  `DROP DATABASE … WITH (FORCE)`, which needs PostgreSQL 13 or later. If the
  connection Secret is already gone (for example when the whole namespace is
  deleted) the database is left in place with a `DatabaseNotDropped` event.
  With it go the databases the instance's restores kept for an undo
  (`status.previous.database`), under the same tag check.
- `spec.database.name` and the `nameFromSecret` selector are immutable so a
  rename can never orphan or drop the wrong database. Changing the value inside
  the Secret that `nameFromSecret` points at stops the instance instead: phase
  `Failed`, `Degraded` reason `DatabaseNameChanged`.
- `spec.odooFilestore.deletionPolicy` works the same way for the filestore PVC
  (Retain by default). The PVC is created once: changing
  `spec.odooFilestore.size` or `storageClassName` later does not change it.
