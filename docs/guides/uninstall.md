# Uninstall

Remove your instances first, while the operator still runs: it is the
operator that applies their deletion policies.

```sh
kubectl delete odoodeployments --all --all-namespaces
```

What stays behind depends on each instance's policies:

- The database is dropped only with `spec.database.deletionPolicy: Delete`,
  and only when the operator created it (see [Databases](../concepts/databases.md)).
- The filestore volume is kept unless `spec.odooFilestore.deletionPolicy` is
  `Delete`.
- Backups are never deleted: deleting an instance or an `OdooBackup` leaves
  the snapshots in their stores. The instance's repositories are no longer
  checked or pruned either, since store tasks only cover instances that list
  the store.
- The databases and folders that restores kept for undo
  (`<db>_prev_<suffix>`) stay; drop them by hand.
- Deleting an instance waits for all its pods to stop, including a running
  backup, restore, init or upgrade Job, whatever its deletion policies, so
  that none is cut short. A Job that never started a container, and the
  image check of a new image, are deleted instead.

Then remove the operator, its CRDs and RBAC:

```sh
kubectl delete -f https://github.com/MohanadAbugharbia/odoo-operator/releases/latest/download/install.yaml
```

Deleting the CRDs deletes every remaining `OdooDeployment`, `OdooBackup`,
`OdooRestore` and `OdooBackupStore` with them. Use the release you installed
if it is not the latest.
