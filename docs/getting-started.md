# Getting started

## Install the operator

Apply the release bundle; it installs the CRDs and runs the manager in `odoo-operator-system`:

```sh
kubectl apply -f https://github.com/MohanadAbugharbia/odoo-operator/releases/latest/download/install.yaml
```

## Run the sample

The repository's [samples](https://github.com/MohanadAbugharbia/odoo-operator/tree/main/config/samples)
install [CloudNativePG](https://cloudnative-pg.io/), a one-instance PostgreSQL
cluster, its credentials and an `OdooDeployment` in the `odoo-sample`
namespace. From a clone of the repository:

```sh
kubectl apply -k config/samples/
kubectl -n odoo-sample get odoodeployment -w
```

The instance goes through `Pending` and `Initializing` (its init Job installs
the modules) to `Running`; [Lifecycle](concepts/lifecycle.md) explains each
phase. Read the sample's comments for what each field does, and the [API
reference](reference/api.md) for every field.

## Next steps

- [Upgrade Odoo](guides/upgrades.md) to a new image.
- [Set up a backup store](guides/backup-stores.md) and [take
  backups](guides/backups.md).
- Turn on the [maintenance page](guides/maintenance-page.md).
- Scrape the [metrics](guides/monitoring.md).
