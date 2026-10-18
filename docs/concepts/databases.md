# Databases

How the operator decides whether it owns an instance's database, and when it may drop it.

- On the first reconcile the operator looks the database up. If it exists it is
  **adopted** (`status.database.provisionedBy: external`). If it is missing and
  `spec.database.createPolicy` is `IfNotExists` (default) the operator creates
  it — `CREATE DATABASE … ENCODING 'unicode' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0`,
  the same shape Odoo uses — and tags it with the comment
  `odoo-operator:<namespace>/<name>` (`provisionedBy: operator`).
- `spec.database.deletionPolicy: Delete` adds the finalizer
  `odoo.abugharbia.com/database`. On deletion the operator stops the pods and
  drops the database **only if** it recorded `provisionedBy: operator` **and**
  the database comment still equals its tag. A CR pointing at a pre-existing
  database never drops it, whatever the policy says.
- `spec.database.name` (and `nameFromSecret`) are immutable so a rename can
  never orphan or drop the wrong database.
- `spec.odooFilestore.deletionPolicy` works the same way for the filestore PVC
  (Retain by default).
