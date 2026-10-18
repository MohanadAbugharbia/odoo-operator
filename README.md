# Odoo-Operator

A Kubernetes operator that runs [Odoo](https://www.odoo.com/) through its whole
lifecycle: database, module installation, configuration, upgrades, backups and
restores, all from one `OdooDeployment` custom resource.

**Documentation: [odoo-operator.abugharbia.com](https://odoo-operator.abugharbia.com/)**

## Features

| Feature | Description |
|---|---|
| Deploy | Odoo Deployments, Services and the filestore volume, from one resource ([lifecycle](https://odoo-operator.abugharbia.com/concepts/lifecycle/)) |
| Module installation | The database is initialised and modules installed in a Job before Odoo starts |
| Configure Odoo | `odoo.conf` rendered from the spec ([configuration](https://odoo-operator.abugharbia.com/reference/configuration/)) |
| Database provisioning | Create the database when missing, adopt it when it exists, drop it only if the operator created it ([databases](https://odoo-operator.abugharbia.com/concepts/databases/)) |
| Upgrades | `-u` Jobs on image changes or on demand; a new image is pulled before anything stops ([upgrade Odoo](https://odoo-operator.abugharbia.com/guides/upgrades/)) |
| Maintenance page | An "updating" page in English or Arabic while Odoo is installed, upgraded or started ([maintenance page](https://odoo-operator.abugharbia.com/guides/maintenance-page/)) |
| Backup | Database and filestore backed up together, consistently, encrypted, to S3, SFTP, NFS or a PVC; scheduled, thinned and copied ([backups](https://odoo-operator.abugharbia.com/guides/backups/)) |
| Restore | In place, checked before the swap, with an undo; scheduled restore drills prove backups restore ([restore](https://odoo-operator.abugharbia.com/guides/restore/)) |
| Metrics | Prometheus metrics and example alerts for instances, Jobs, backups and stores ([metrics](https://odoo-operator.abugharbia.com/reference/metrics/)) |
| Safety defaults | `list_db = False` + `dbfilter` lockdown, retained data by default, probes, pod security context, Job deadlines, CEL-validated spec, `Ready`/`Degraded` conditions |

Feel free to request more features by creating an [issue](https://github.com/MohanadAbugharbia/odoo-operator/issues/new?template=Blank+issue).

## Installation

```sh
kubectl apply -f https://github.com/MohanadAbugharbia/odoo-operator/releases/latest/download/install.yaml
```

Then follow [Getting started](https://odoo-operator.abugharbia.com/getting-started/), which runs the sample
`OdooDeployment` with its own PostgreSQL. Upgrading from an older release?
Read [Upgrading the operator](https://odoo-operator.abugharbia.com/operations/upgrading-the-operator/).

## Contributing

Please first create an issue with your planned contribution. Anything and
everything is welcome. [Development](https://odoo-operator.abugharbia.com/contributing/development/) covers the
tests and running a build on a cluster, and [Writing the
docs](https://odoo-operator.abugharbia.com/contributing/docs/) the documentation site, which is built from
[`docs/`](docs/). Run `make help` for every `make` target.

## License

MIT License

Copyright (c) 2026 Mohanad Abugharbia

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
