# Getting started

This page takes you from an empty cluster to Odoo open in your browser and a
first backup, in about fifteen minutes. It uses the repository's samples,
which bring their own PostgreSQL.

## Before you begin

- A Kubernetes cluster, 1.28 or newer, and `kubectl` with cluster-admin
  rights (the install creates CRDs and cluster roles).
- A default StorageClass. The sample asks for `storageClassName: standard`;
  if your cluster has no class of that name, change
  `spec.odooFilestore.storageClassName` in
  `config/samples/odoo_v1_odoodeployment.yaml`.
- A clone of the repository, for the samples:

    ```sh
    git clone https://github.com/MohanadAbugharbia/odoo-operator.git
    cd odoo-operator
    ```

## 1. Install the operator

```sh
kubectl apply -f https://github.com/MohanadAbugharbia/odoo-operator/releases/latest/download/install.yaml
kubectl -n odoo-operator-system rollout status deploy/odoo-operator-controller-manager
```

This installs the four CRDs (`OdooDeployment`, `OdooBackupStore`,
`OdooBackup`, `OdooRestore`) and runs the manager in
`odoo-operator-system`.

## 2. Run the sample

!!! warning "The sample installs CloudNativePG"
    `config/samples` also installs the [CloudNativePG](https://cloudnative-pg.io/)
    operator, to run a one-instance PostgreSQL cluster: the operator itself
    into `odoo-sample`, its CRDs and webhook configurations cluster-wide.
    Skip the sample on a cluster that already runs CloudNativePG, and point
    an `OdooDeployment` at your own PostgreSQL instead.

```sh
kubectl create namespace odoo-sample
kubectl kustomize config/samples | kubectl apply --server-side --force-conflicts -f -
```

Server-side apply is needed because CloudNativePG's CRDs are too large for
the annotation a client-side `kubectl apply` writes.

## 3. Wait for it to run

```sh
kubectl -n odoo-sample get odoodeployment -w
```

The instance goes through `Pending` (until the database is reachable) and
`Initializing` (its `odoodeployment-sample-init` Job installs the modules) to
`Running`:

```text
NAME                    PHASE     READY   IMAGE                       DATABASE   AGE
odoodeployment-sample   Running   True    mohanadabugharbia/odoo:18   odoo       4m
```

[Lifecycle](concepts/lifecycle.md) explains each phase. If it stays in one,
see [Troubleshooting](operations/troubleshooting.md).

## 4. Open Odoo

The operator names everything after the OdooDeployment's `metadata.name`:
here the Service is `odoodeployment-sample-http`.

```sh
kubectl -n odoo-sample port-forward svc/odoodeployment-sample-http 8069:8069
```

Open <http://localhost:8069> and log in as `admin` with the password
`admin`, the user Odoo creates when it initialises a database. Change it
in Odoo's preferences.

The *master* password, which protects Odoo's database manager, is a
different one: the operator generates it into the Secret
`odoodeployment-sample-admin-password`. The database manager is off by
default (`spec.config.listDb: false`).

To reach Odoo without a port-forward, see [Expose Odoo](guides/expose.md).

## 5. Take a first backup

Backups go to an [`OdooBackupStore`](guides/backup-stores.md). For a first
try, a volume in the cluster will do:

```sh
kubectl -n odoo-sample create secret generic backup-key \
  --from-literal=password="$(openssl rand -base64 32)"
```

```yaml
# backup.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: backups
  namespace: odoo-sample
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 5Gi
---
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackupStore
metadata:
  name: local
  namespace: odoo-sample
spec:
  pvc:
    claimName: backups
  encryptionKeySecret:
    name: backup-key
    key: password
```

```sh
kubectl apply -f backup.yaml
kubectl -n odoo-sample get odoobackupstore local -w     # wait for READY true
kubectl -n odoo-sample patch odoodeployment odoodeployment-sample --type merge \
  -p '{"spec":{"backup":{"stores":["local"]}}}'
```

Then take a backup:

```yaml
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackup
metadata:
  name: first
  namespace: odoo-sample
spec:
  odooDeployment: odoodeployment-sample
```

```sh
kubectl -n odoo-sample get odoobackup first -w
```

(The snapshot ID is shortened here; kubectl shows all 64 characters.)

```text
NAME    INSTANCE                PHASE       SNAPSHOT        STORE   COMPLETED   AGE
first   odoodeployment-sample   Completed   3f9c2a1b5e…     local   20s         1m
```

A store inside the cluster disappears with it. For real backups, follow [Set
up production backups](guides/production-backups.md).

## 6. Clean up

```sh
kubectl -n odoo-sample delete odoodeployment odoodeployment-sample   # first, while PostgreSQL runs: drops its database
kubectl kustomize config/samples | kubectl delete --ignore-not-found -f -
kubectl delete namespace odoo-sample
```

The second command removes everything the sample installed, including the
CloudNativePG CRDs and webhook configurations it put cluster-wide.

To remove the operator too, see [Uninstall](guides/uninstall.md).

## Next steps

- [Set up production backups](guides/production-backups.md): offsite
  stores, schedules, copies and restore drills.
- [Upgrade Odoo](guides/upgrades.md) to a new image.
- [Expose Odoo](guides/expose.md) through an Ingress.
- [Monitoring and alerts](guides/monitoring.md).
