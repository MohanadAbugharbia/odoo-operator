# Lifecycle

An `OdooDeployment` moves through `status.phase`:

| Phase | Meaning |
|---|---|
| `Pending` | The database connection or the database itself is not settled yet (see the `Degraded` condition for the reason). |
| `Initializing` | The `<name>-init` Job installs `spec.modules` (`-i`, with `--load-language` from `spec.config.loadLanguages`) on `spec.image`. No Deployment exists yet. |
| `Upgrading` | The new image has been pulled once (see [Upgrades](../guides/upgrades.md)); the Deployment is scaled to zero and a `<name>-upgrade-<hash>` Job runs `-i <new modules>` / `-u <spec.upgrade.modules>` on it. |
| `Running` | The Deployment runs `status.appliedImage`; `Ready` is True once the requested replicas are available (or `spec.replicas` is 0). |
| `BackingUp` | An offline backup runs; Odoo is stopped (see [How backups work](backups.md)). An online backup leaves the phase alone. |
| `Restoring` | An OdooRestore runs; Odoo is stopped until it finishes. |
| `Failed` | A maintenance Job failed. Inspect it with `kubectl logs job/<name>`; deleting the Job retries it. |

`status.currentInitJob.startedAt` records when the running Job was created and
`status.lastMaintenanceDuration` how long the last successful upgrade took.

Conditions: `Ready`, `DatabaseReady`, `Initialized` and `Degraded` (True only when
something is wrong; its reason names the problem, e.g. `DatabaseMissing`,
`InitJobFailed`, `QuotaExceeded`, `ImagePullFailed`, `DatabaseDropFailed`).

The `<name>-http` and `<name>-poll` Services are created on the first
reconcile, before the database and the init Job, so an Ingress pointing at
them can be applied together with the CR. They have no endpoints until the
Deployment's pods are ready, unless the [maintenance page](../guides/maintenance-page.md)
is enabled.
