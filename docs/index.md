# Odoo Operator

The Odoo Operator runs [Odoo](https://www.odoo.com/) on Kubernetes. You
describe an instance in one `OdooDeployment`, and the operator does the rest:

- it creates or adopts the PostgreSQL database;
- it installs the modules you list, in a Job, before Odoo starts;
- it renders `odoo.conf` from the spec;
- it runs the Deployment, Services and filestore volume;
- it upgrades the database when the image changes, or when you ask it to;
- it serves a maintenance page while Odoo is installed, upgraded or started;
- it backs up the database and filestore to S3, SFTP, NFS or a volume, and
  restores them in place, with an undo.

## Install

```sh
kubectl apply -f https://github.com/MohanadAbugharbia/odoo-operator/releases/latest/download/install.yaml
```

Then start from the sample
[OdooDeployment](https://github.com/MohanadAbugharbia/odoo-operator/blob/main/config/samples/odoo_v1_odoodeployment.yaml).

## Where to read next

!!! note "This site is new"
    The guides still live in the project's
    [README](https://github.com/MohanadAbugharbia/odoo-operator#readme) and
    move here page by page. The reference below is generated from the code
    and is complete for this version.

- [README: Lifecycle](https://github.com/MohanadAbugharbia/odoo-operator#lifecycle):
  the phases an instance goes through.
- [README: Upgrades](https://github.com/MohanadAbugharbia/odoo-operator#upgrades)
  and [Maintenance page](https://github.com/MohanadAbugharbia/odoo-operator#maintenance-page).
- [README: Backups and restores](https://github.com/MohanadAbugharbia/odoo-operator#backups-and-restores).
- [API reference](reference/api.md): every field of every resource.
- [Metrics reference](reference/metrics.md): every series on `/metrics`.
