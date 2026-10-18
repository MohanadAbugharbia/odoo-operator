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

## Start here

- [Getting started](getting-started.md): install the operator and run the sample.
- [Lifecycle](concepts/lifecycle.md): the phases an instance goes through.
- [How backups work](concepts/backups.md), then [set up a store](guides/backup-stores.md).
- [API reference](reference/api.md): every field of every resource.
- [Metrics reference](reference/metrics.md): every series on `/metrics`.
