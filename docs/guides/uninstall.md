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
  the snapshots in their stores.

Then remove the operator, its CRDs and RBAC:

```sh
kubectl delete -f https://github.com/MohanadAbugharbia/odoo-operator/releases/latest/download/install.yaml
```

Deleting the CRDs deletes every remaining `OdooDeployment`, `OdooBackup`,
`OdooRestore` and `OdooBackupStore` with them. Use the release you installed
if it is not the latest.
