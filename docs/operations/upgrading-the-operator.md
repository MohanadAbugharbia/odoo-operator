# Upgrading the operator

Read the notes of every version between the one you run and the one you
install.

## 0.3.0

- **`list_db` is now `False` by default** and `dbfilter = ^<db_name>$` is
  rendered, so an instance only sees its own database. Set
  `spec.config.listDb: true` (and optionally `dbFilter`) to keep the database
  manager.
- `spec.database.ssl` is rendered as `db_sslmode = require` / `disable`
  (previously it was ignored and Odoo used `prefer`).
- `spec.database.name` and `spec.database.nameFromSecret` are immutable.
- `spec.replicas`, `spec.config.workers`, `maxCronThreads`, `withoutDemo` and
  `proxyMode` are pointer fields: `0` and `false` are now honoured.
- Existing CRs roll their pods once (new labels, config-hash annotation,
  probes) and adopt the running image as `status.appliedImage` without a Job.
  Their database is adopted as `external` and is never dropped.
- The `OperatorDegraded`/`OperatorSucceeded` conditions are replaced by
  `Ready`, `DatabaseReady`, `Initialized` and `Degraded`.
- The admin and config Secrets and both Services are now owned by the CR and
  are garbage collected with it.
