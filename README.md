# Odoo-Operator
A Kubernetes operator that manages the full lifecycle of Odoo deployments — including database initialization, secrets, persistent storage, and services — via a single `OdooDeployment` custom resource.

Check out the sample [OdooDeployment](config/samples/odoo_v1_odoodeployment.yaml) for an example of how to use this operator.

Check out the [Odoo](https://www.odoo.com/) website for more information on Odoo.

## Features

| Feature | Status | Description |
|---|---|---|
| Deploy | available | Create and manage Odoo Deployments, Services, and PVCs |
| Module installation | available | Initialize the database and install Odoo modules via a Kubernetes Job |
| Configure Odoo | available | Manage `odoo.conf` settings dynamically via the CR spec (`listDb`, `dbFilter`, `serverWideModules`, `loadLanguages`, `extraOptions`, …) |
| Database provisioning | available | Create the PostgreSQL database when it is missing (`createPolicy`), adopt it when it exists, and drop it on deletion only when the operator created it (`deletionPolicy`) |
| Upgrades | available | Run `-u` maintenance Jobs automatically on image changes or on demand through `spec.upgrade.token`; the Deployment is scaled to zero while the Job runs |
| Maintenance page | available | Serve an "updating" page in English or Arabic on the http Service while Odoo is installed, upgraded or started, instead of an empty Service (`spec.maintenancePage`) |
| Metrics | available | Prometheus metrics for every OdooDeployment's phase, conditions and Jobs, next to the controller-runtime defaults (see [Metrics](#metrics)) |
| Safety defaults | available | `list_db = False` + `dbfilter` lockdown, retained filestore and database by default, HTTP probes, resources, env, pod security context, Job deadlines/TTL, CEL-validated spec, `Ready`/`Degraded` conditions and `kubectl get` columns |
| Backup | available | Back up the database and filestore together, consistently, to S3, SFTP, NFS or a PVC (`OdooBackupStore`, `OdooBackup`); encrypted and deduplicated with restic; attachments the filestore lost are taken from the previous backup (see [Backups and restores](#backups-and-restores)) |
| Restore | available | Restore a backup in place (`OdooRestore`): loaded and checked beside the live data, then swapped in; the replaced data is kept for `keepPrevious` so the restore can be undone |

Feel free to request more features by creating an [issue](https://github.com/MohanadAbugharbia/odoo-operator/issues/new?template=Blank+issue)

## Installation

Users can just run kubectl apply -f <URL for YAML BUNDLE> to install the project, i.e.:

```sh
kubectl apply -f https://github.com/mohanadAbugharbia/odoo-operator/releases/latest/download/install.yaml
```

## Lifecycle

An `OdooDeployment` moves through `status.phase`:

| Phase | Meaning |
|---|---|
| `Pending` | The database connection or the database itself is not settled yet (see the `Degraded` condition for the reason). |
| `Initializing` | The `<name>-init` Job installs `spec.modules` (`-i`, with `--load-language` from `spec.config.loadLanguages`) on `spec.image`. No Deployment exists yet. |
| `Upgrading` | The new image has been pulled once (see [Upgrades](#upgrades)); the Deployment is scaled to zero and a `<name>-upgrade-<hash>` Job runs `-i <new modules>` / `-u <spec.upgrade.modules>` on it. |
| `Running` | The Deployment runs `status.appliedImage`; `Ready` is True once the requested replicas are available (or `spec.replicas` is 0). |
| `BackingUp` | An offline backup runs; Odoo is stopped (see [Backups and restores](#backups-and-restores)). An online backup leaves the phase alone. |
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
Deployment's pods are ready, unless the [maintenance page](#maintenance-page)
is enabled.

### Maintenance page

With `spec.maintenancePage.enabled: true`, a browser opening the instance
while Odoo cannot answer gets a status page instead of a blank screen:

```yaml
spec:
  maintenancePage:
    enabled: true
    title: Ababiel            # default "Odoo"
    titleTranslations:
      ar: أبابيل
    accentColor: "#f2800d"    # default "#714b67"
    logo: data:image/svg+xml;base64,…   # optional, data URIs only
```

- Whenever Odoo's Deployment has no available replica (first setup, an
  upgrade, a restart) and `spec.replicas` is not 0, the operator runs a
  `<name>-maintenance` Deployment (one pod, 10m CPU / 32Mi) and points the
  `<name>-http` Service's selector at it. Once Odoo has an available replica
  the selector moves back and the page scales to zero. The `<name>-poll`
  Service always stays on Odoo.
- The pod runs the operator's own image (`odoo-operator maintenance-page`), so
  there is nothing extra to build or pin. The operator reads its image from
  its own pod, retrying on each reconcile until it succeeds (a failure shows
  as a `MaintenancePageImageUnknown` warning event on the OdooDeployment);
  `--maintenance-page-image` overrides it.
- The page needs no access to the API server: after every reconcile the
  operator writes what the page shows into the `<name>-maintenance`
  ConfigMap, which the pod mounts (no ServiceAccount token). On each change
  it also stamps the status hash on the page pods
  (`odoo.abugharbia.com/maintenance-status`), which makes the kubelet refresh
  the mount at once instead of on its next periodic sync, up to a minute later.
- It answers like a server that is down, so Odoo's own clients keep their
  offline behaviour: a page navigation (`Sec-Fetch-Mode: navigate`) gets the
  page with status 503, and every other request (RPC, assets, `/web/health`,
  service worker fetches) gets an empty 503. Nothing is cacheable.
- The page shows the phase as steps (preparing the database, installing,
  updating, starting), the elapsed time and, for upgrades, an estimate from
  `status.lastMaintenanceDuration`. It polls `/__maintenance/status.json` and
  `/web/health` every 3 seconds, and reopens the URL that was asked for as soon
  as Odoo answers. A failed Job shows only "Please contact your
  administrator"; the reason stays in the CR's conditions and events.
- English or Arabic (right to left) is picked from the browser's
  `Accept-Language`; it follows the device's light or dark mode, and loads no
  external fonts or scripts.

### Database provenance

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

### Upgrades

- `spec.upgrade.onImageChange: true` (default): every `spec.image` change runs
  an upgrade Job with `-u <spec.upgrade.modules>` before the Deployment rolls
  to the new image. New entries in `spec.modules` are installed in the same Job.
- Empty `spec.upgrade.modules` rolls the new image without a Job.
- **A new image is pulled before anything stops.** When `spec.image` changes on
  a running instance, a short `<name>-image-<hash>` Job runs
  `<odooCommand> --version` on it first, without the filestore or the config.
  Only once it succeeds is the Deployment scaled down (or rolled). Until then
  the old image keeps serving; if the image cannot be pulled `Degraded` is
  `ImagePullFailed` (or `ImageCheckFailed` when it pulled but did not run), and
  a later `spec.image` replaces the check. This is what keeps an instance up
  when its image tag is published before the image is pushed, or never is.
- An init or upgrade Job whose pod cannot pull its image is reported as
  `ImagePullFailed`. If `spec.image` changes meanwhile, the Job is replaced
  by one for the new image: its container never started, so nothing touched
  the database.
- For production set `onImageChange: false` and bump `spec.upgrade.token` to
  run the upgrade on demand.
- `spec.jobs` bounds the Jobs (`activeDeadlineSeconds`, `ttlSecondsAfterFinished`,
  `backoffLimit`).

### Container command

`spec.odooCommand` (default `["odoo"]`) plus `-c /opt/odoo/odoo.conf` becomes
the container `command`, which **replaces the image ENTRYPOINT**. Images whose
entrypoint script injects database flags (e.g. the official image's
`entrypoint.sh`) are bypassed on purpose: the operator's rendered `odoo.conf`
is the only configuration source. The image's own `addons_path` and
`server_wide_modules` must therefore be re-expressed in
`spec.config.extraAddonsPaths` and `spec.config.serverWideModules`.

## Backups and restores

Backups need three objects: a store, the instance pointing at it, and a
backup.

```yaml
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackupStore
metadata:
  name: offsite
  namespace: shop
spec:
  s3:                                 # or sftp, nfs, pvc: exactly one
    endpoint: https://s3.example.com
    bucket: odoo-backups
    path: production
    credentialsSecret: { name: s3-credentials }   # AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
  encryptionKeySecret: { name: backup-key, key: password }
---
# in the OdooDeployment
spec:
  backup:
    stores: [offsite]
    consistency: Online               # default; Offline stops Odoo while it runs
---
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackup
metadata:
  name: my-odoo-2026-10-17
  namespace: shop
spec:
  odooDeployment: my-odoo
```

**Stores.** Every store holds [restic](https://restic.net/) repositories, one
per instance under `<path>/<namespace>/<deployment>/`, encrypted with the key
in `encryptionKeySecret` (keep a copy of it outside the cluster: without it no
backup can be read). The kinds:

| Kind | Fields | Secret |
|---|---|---|
| `s3` | `endpoint` (empty for AWS), `region`, `bucket`, `path`, `forcePathStyle` | `credentialsSecret`: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` |
| `sftp` | `host`, `port` (22), `user`, `path` | `sshSecret`: `id_ed25519` (private key) and `known_hosts` (the server's host key; nothing else is trusted) |
| `nfs` | `server`, `path` | |
| `pvc` | `claimName`, `subPath` | |

The operator probes each store with a short Job that writes, reads and deletes
a file (`.odoo-operator-probe-<random>`), on every change to the store or its
Secrets and every six hours. `kubectl get odoobackupstore` shows `Ready`; a
store that fails says why in its `Ready` condition and in an event, and is
probed again every two minutes until it passes. The last failed probe's Job is
kept for its log.

**A backup** is one restic snapshot holding `dump.sql` (a plain `pg_dump`
taken with a `pg_dump` of the server's major), `manifest.json` (Odoo's own
format, plus an `operator` key) and the `filestore/`, so it is also what
Odoo's database manager restores. Online backups run beside Odoo: the dump,
the attachment list and the row counts all come from one database snapshot, so
they agree with each other. Files that disappeared from the filestore since
(Odoo's attachment cleanup) are taken from the previous backup; attachments in
neither are listed in `status.missingFileNames` and the backup ends
`CompletedWithWarnings`. An online backup waits up to two hours for Odoo's
filestore cleanup cron to finish when it is running or due within ten minutes
(phase `WaitingForFilestoreCleanup`). With a `ReadWriteOncePod` data volume,
or `consistency: Offline`, Odoo is stopped while the backup runs.

| `OdooBackup` phase | Meaning |
|---|---|
| `Pending` | Waiting for the instance: one backup, restore or cleanup runs at a time, and backups wait for init, upgrades and rollouts. |
| `WaitingForFilestoreCleanup` | Odoo's filestore cleanup runs or is due; tried again every two minutes. |
| `Running` | The Job runs. |
| `Completed` / `CompletedWithWarnings` | `status.snapshotID` holds it. |
| `Failed` | `status.message` names the step that failed and the Job to read. |

Deleting an OdooBackup does not delete the snapshot, and deleting an
instance never deletes its backups.

**A restore** replaces the instance's database and filestore in place:

```yaml
apiVersion: odoo.abugharbia.com/v1
kind: OdooRestore
metadata:
  name: back-to-friday
  namespace: shop
spec:
  odooDeployment: my-odoo
  backup: my-odoo-2026-10-17        # or snapshotID: <id> in the instance's store
  keepPrevious: 72h                 # default spec.backup.restore.keepPrevious (72h)
```

Odoo is stopped; the backup is loaded into `<db>_restore_<suffix>` and
`filestore/<db>_restore_<suffix>` beside the live data and checked (every
table's row count against the backup, every attachment's file, a sample of
files against their checksum). Only then are the names swapped: the live data
becomes `<db>_prev_<suffix>` and the restored data takes its place, and Odoo
starts on the image recorded in the backup. A restore that fails before the
swap leaves the live data untouched (`Failed`); one that fails during the swap
is retried until it finishes, with Odoo stopped and the instance `Degraded`
(`RestoreIncomplete`). A restore takes precedence over everything else and
replaces a failed upgrade Job, which makes it the way back from a failed
upgrade. Modules in `spec.modules` that the backup lacks are installed again
afterwards. If `spec.image` differs from the backup's image the usual upgrade
follows; set `spec.image` to the backup's image first to stay on it.

The replaced data is kept until `status.previous.keptUntil`, then deleted. Until
then the restore can be undone:

```yaml
spec:
  odooDeployment: my-odoo
  undo: back-to-friday
```

Only the latest restore can be undone. Deleting an OdooRestore deletes the data
it kept.

**Schedules and retention.** Backups can run on a schedule, and old ones are
deleted after each backup:

```yaml
spec:
  backup:
    stores: [offsite, cold]           # the first is written; the others get copies
    schedule: "0 3 * * *"             # cron or @daily; empty (default) = no scheduled backups
    timeZone: Europe/Berlin           # of the schedules and the retention periods (default UTC)
    retention:                        # defaults: daily 7, weekly 4, monthly 12
      daily: 7
      weekly: 4
      monthly: 12
      within: 2d                      # also keep everything from the last 2 days (y, m, d, h)
      manual: 10                      # newest manual and pre-upgrade backups kept; 0 (default) keeps all
    drill:
      schedule: "0 5 * * 0"           # restore drill, see below
```

A scheduled backup is an ordinary OdooBackup named `<instance>-<yyyymmdd-hhmm>`
(the time in `timeZone`) that waits in the instance's queue like any other. A
time missed while the operator was down is taken once when it is back; a time
that comes while the previous scheduled backup is still unfinished is skipped
with an event. `status.backup` shows the last and next scheduled times, and the
`BackupSchedule` condition reports a schedule that cannot be read.

Retention runs in the backup Job, after the new backup is verified. The keep
rules are restic's (`last`, `hourly`, `daily`, `weekly`, `monthly`, `yearly`,
`within`, with restic's meaning), but the periods are counted in `timeZone`,
and they apply to scheduled backups; manual and pre-upgrade backups each keep
their newest `manual`. Never deleted: the newest backup, a backup with
`spec.keep: true` (it does not use up a slot of the rules either), a snapshot
an unfinished restore needs, and snapshots the operator did not take. The
OdooBackups of deleted snapshots are deleted too; a retention that fails leaves
the backup `Completed` with a warning. `retention.keepAll: true` turns it off.
Deleting the snapshot frees no space by itself: the store's prune does (below).

**Copies.** Each backup is copied (`restic copy`) to every store after the
first in `spec.backup.stores`, in the same Job, and retention runs in each
copy too. A store that is missing or not ready, or a copy that fails, leaves
the backup `PartiallyCompleted` with `status.copies` saying why; the backup in
the first store is still good. A restore (or drill) whose backup's store is
not ready restores a copy from a ready store instead, with an event saying so.
Stores may use different kinds and different encryption keys.

**Checking and pruning a store.** On their own schedules a store's repositories
are checked (`restic check`) and pruned (`restic prune`, which frees the space
of deleted backups):

```yaml
spec:                                 # OdooBackupStore
  check:
    schedule: "0 4 * * 0"
    readDataParts: 7                  # each check reads 1/7 of the data, all of it over 7 checks;
                                      # 1 = everything every time, 0 = structure only
  prune:
    schedule: "0 2 * * 6"
  timeZone: Europe/Berlin
```

One Job runs at a time per store, prune first when both are due, over the
repositories of every instance in the namespace that lists the store. The
outcome is in `status.lastCheck` and `status.lastPrune` (with the part read,
the repositories and what they hold); a failed task keeps its Job for its log
and warns. A backup that starts while a prune holds a repository's lock waits
up to half an hour for it.

**Restore drills.** A drill proves the newest backup can be restored, without
touching the instance: on `spec.backup.drill.schedule` it waits in the
instance's queue, then one Job restores the newest finished backup into a
scratch database `<db>_drill_<suffix>` and a scratch filestore on an
`emptyDir` (never the data volume), runs the same checks as a restore, loads it
with the backup's own Odoo image (`odoo -d <db>_drill_<suffix>
--stop-after-init`, through `sh`), and drops the scratch database. Odoo keeps
serving throughout. The outcome is in `status.backup.lastDrill`
(`lastSuccessfulDrillTime` keeps the last pass); a failed drill keeps its Job
(`kubectl logs job/<name> --all-containers`). The scratch filestore needs node
disk the size of the filestore; `drill.scratchSizeLimit` caps it.

**The backup image.** The Jobs run `ghcr.io/mohanadabugharbia/odoo-operator-backup`
at the operator's own tag (derived from the operator's image; `--backup-image`
overrides it). It carries the PostgreSQL 14–18 clients, restic, rclone and the
`odoo-backup` agent, and runs as the instance's pod security context with no
service account token.

## Metrics

The manager serves `/metrics` (by default on `:8443`, HTTPS, with Kubernetes
authentication and authorization; `--metrics-bind-address=:8080
--metrics-secure=false` serves plain HTTP instead). Besides the
controller-runtime defaults (`controller_runtime_reconcile_*`, `workqueue_*`,
`rest_client_*`, Go and process metrics) it exports:

**State, read from every OdooDeployment's status at scrape time.** Only the
elected leader reports these, and a deleted OdooDeployment disappears on the
next scrape. Every series has `namespace` and `name` labels.

| Metric | Labels | Value |
|---|---|---|
| `odoo_operator_deployment_info` | `image`, `applied_image`, `database` | 1 |
| `odoo_operator_deployment_phase` | `phase` | 1 for the current `status.phase`, 0 for every other phase |
| `odoo_operator_deployment_condition` | `type`, `status` (`true`/`false`/`unknown`), `reason` | 1, one series per condition |
| `odoo_operator_deployment_condition_last_transition_timestamp_seconds` | `type` | when the condition last changed status |
| `odoo_operator_deployment_replicas_desired` | | `spec.replicas` |
| `odoo_operator_deployment_image_pending` | | 1 while `spec.image` ≠ `status.appliedImage` |
| `odoo_operator_deployment_job_start_timestamp_seconds` | `kind` (`init`/`upgrade`) | creation time of the running Job; absent while none runs |
| `odoo_operator_deployment_last_upgrade_duration_seconds` | | `status.lastMaintenanceDuration` |
| `odoo_operator_deployment_database_ready` | `provisioned_by` | 1 while `DatabaseReady` is true |
| `odoo_operator_backup_last_success_timestamp_seconds` | | when the newest successful backup completed |
| `odoo_operator_backup_last_size_bytes` | `part` (`database`/`filestore`/`added`) | sizes of the newest successful backup |
| `odoo_operator_backup_missing_files` | | attachments the newest successful backup could not include |
| `odoo_operator_backup_last_failed` | | 1 when the most recently finished backup failed |
| `odoo_operator_backup_enabled` | | 1 when `spec.backup.schedule` is set |
| `odoo_operator_backup_last_copy_failed` | `store` | 1 when the most recently finished backup could not be copied to that store |
| `odoo_operator_restore_last_failed` | | 1 when the most recently finished OdooRestore failed |
| `odoo_operator_restore_drill_enabled` | | 1 when `spec.backup.drill.schedule` is set |
| `odoo_operator_restore_drill_last_success_timestamp_seconds` | | when a drill last passed |
| `odoo_operator_restore_drill_last_failed` | | 1 when the latest drill failed |
| `odoo_operator_backup_store_ready` | `store` (no `name`) | 1 when the store's last probe succeeded |
| `odoo_operator_backup_store_task_last_timestamp_seconds` | `store`, `task` (`check`/`prune`) (no `name`) | when the store's latest check or prune finished |
| `odoo_operator_backup_store_task_last_failed` | `store`, `task` (no `name`) | 1 when it failed |
| `odoo_operator_backup_store_stored_bytes` | `store` (no `name`) | what the store's repositories hold, as of its latest check or prune |

**Events, counted by the reconciler.** Each event is counted once, on the
reconcile that causes it: a Job that stays failed, an image that keeps failing
to pull or a drop that keeps failing is not counted again on every requeue.
The per-OdooDeployment counters start at 0 and are removed once the
OdooDeployment is gone.

| Metric | Labels | Counts |
|---|---|---|
| `odoo_operator_jobs_total` | `namespace`, `name`, `kind`, `result` (`succeeded`/`failed`/`superseded`) | finished init and upgrade Jobs |
| `odoo_operator_job_duration_seconds` (histogram) | `kind`, `result` | creation to completion or failure of succeeded and failed Jobs |
| `odoo_operator_image_pull_failures_total` | `namespace`, `name` | times an OdooDeployment became Degraded with `ImagePullFailed` |
| `odoo_operator_database_operations_total` | `operation` (`create`/`adopt`/`drop`), `result` (`succeeded`/`failed`/`refused`) | database operations |
| `odoo_operator_maintenance_page_publish_errors_total` | `namespace`, `name` | failed maintenance page updates |
| `odoo_operator_backups_total` | `kind` (`manual`/`scheduled`/`pre-upgrade`), `result` (`succeeded`/`failed`) | finished backups |
| `odoo_operator_backup_duration_seconds` (histogram) | `kind` | start to completion of successful backups |
| `odoo_operator_restores_total` | `mode` (`restore`/`undo`), `result` (`succeeded`/`failed`) | finished restores |
| `odoo_operator_build_info` | `version` | 1 |

`config/prometheus/backup-alerts.yaml` holds example alerts on these metrics
(stale or failed backups, failed copies, restores and drills, stores that are
not ready or fail their check or prune). Adjust the staleness thresholds to
your schedules.

## Upgrading to 0.3.0

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

## Getting Started as a contributor

### Prerequisites
- go version v1.25.0+
- docker version 17.03+.
- kubectl version v1.28.0+.
- Access to a Kubernetes v1.28.0+ cluster.

### Running the tests

```sh
make test
```

This runs the unit suites and the envtest ones (the Kubernetes binaries are
downloaded on demand). Coverage is written to `cover.out` and measured with
`-coverpkg` across every shipped package, so a helper exercised by another
package's suite is credited to it; measuring per-package instead understated
the project by roughly 25 points.

The `internal/database` tests that need a real PostgreSQL skip unless one is
configured, because they assert the SQL the provisioner emits, the `COMMENT`
that marks operator-owned databases and the refusal to drop a database
carrying somebody else's comment. CI starts a `postgres:16` service for them.
To run them locally against any throwaway server:

```sh
TEST_PG_HOST=127.0.0.1 TEST_PG_PORT=5432 \
TEST_PG_USER=postgres TEST_PG_PASSWORD=postgres make test
```

The backup agent's real-service tests (`internal/backup/agent`) skip without
`TEST_BACKUP_PG_HOST` (the same variables with a `BACKUP_` infix), because
they also need restic, rclone and the client tools of the server's major.
`hack/backup-it.sh` runs them inside the backup image against PostgreSQL, an
SFTP server and an S3 server it starts in Docker: fidelity of a round trip,
the repair from the previous backup, undo, cleanup, and a failure or crash
injected at every step of a restore, which must then either leave the live
data untouched or finish when run again.

```sh
PG_MAJOR=17 hack/backup-it.sh
```

`hack/backup-e2e.sh` backs up, restores and undoes a real Odoo in a kind
cluster through an S3 store and a PVC store. CI runs both.

`make test-e2e` additionally needs Docker and kind, and is not part of CI.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/odoo-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/odoo-operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Continuous integration

`.github/workflows/ci.yml` runs on every pull request and on pushes to `main`,
in these jobs:

- **Unit and envtest** — `make test` (the envtest suites against a real API
  server), a check that the generated files in `api/v1/zz_generated.deepcopy.go`,
  `config/crd` and `config/rbac` are in sync with the types, and a
  `make build-installer` of the release bundle.
- **Backup on PostgreSQL 16–18** — `hack/backup-it.sh` once per major.
- **Backup e2e (kind)** — `hack/backup-e2e.sh`.
- **Lint** — `make lint` (golangci-lint).
- **Images** — on pull requests from this repository, pushes the operator and
  backup images (amd64 and arm64) tagged `sha-<head sha>` and `pr-<number>`, so
  a branch can be tried on a real cluster before it is released.

### Test results

The test job runs `go test` with `-json` and renders the event stream into a
Markdown report with `hack/test-summary.py`. The report goes to the job summary
on every run and, on pull requests, into a single sticky comment that is
rewritten in place each time (header `go-test-results`), so a PR always shows
the state of its latest run rather than a trail of comments. The full stream and
the coverage profile (plus a rendered `coverage.html`) are uploaded as artifacts
for seven days, and the report links to both.

Two details of the count are worth knowing when reading it:

- **Ginkgo suites are counted in specs, not in Go tests.** `internal/controller`
  and `internal/controller/reconcileloops` each expose one Go test to the
  toolchain (`TestControllers`) that carries the whole suite, so counting the
  stream as-is would report 2 tests for ~70 specs. Where a package's output
  carries Ginkgo's own `Ran N of M Specs` summary, those numbers are used and
  the wrapper test is not counted.
- **Only leaf tests count.** A table-driven parent with subtests contributes its
  subtests, not itself.

A package that fails without a failing test — a build error, a panic outside a
test, a `TestMain` failure — is reported as a failure with its output, not
silently dropped: `go test -json` reports a build failure as a separate event
shape that names no package.

To get the same report locally:

```sh
make test-summary   # writes go-test.json, cover.out and test-summary.md
```

## Contributing

Please first create an issue with your planned contribution. Anything and everything is welcome.

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

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

