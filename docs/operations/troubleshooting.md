# Troubleshooting

## Where to look first

```sh
kubectl -n <ns> get odoodeployment <name>             # phase, Ready
kubectl -n <ns> describe odoodeployment <name>        # conditions and events
kubectl -n <ns> get events --field-selector involvedObject.name=<name>
kubectl -n <ns> get odoobackup,odoorestore,odoobackupstore
```

When something is wrong, the instance's **`Degraded`** condition is `True`
and its reason names the problem; a failed Job's event and message include
the `kubectl logs` command to read it.

## The instance

### Stuck in Pending

Read the `Degraded` reason:

| Reason | Meaning | Fix | Retried |
|---|---|---|---|
| `InvalidSpec` | A key in `spec.config.extraOptions` is not a valid option name (`^[a-z_][a-z0-9_]*$`). | Fix the key. | On the spec change |
| `DatabaseConnectionFailed` ("could not resolve…") | A Secret or key named by `spec.database.*FromSecret` (or `passwordFromSecret`) does not exist. | Create the Secret or key. | Every 30 s, and when the Secret changes |
| `DatabaseConnectionFailed` | PostgreSQL is unreachable or refuses the credentials. | Fix the host, credentials or network. | Every 30 s |
| `DatabaseProvisioning` | The operator is about to create the database. | — | Every 10 s |
| `DatabaseCreateFailed` | `CREATE DATABASE` failed. | Give the role `CREATEDB`, or create the database yourself. | Every 30 s |
| `DatabaseMissing` | The database does not exist and `createPolicy` is `Never`. | Create it, or set `createPolicy: IfNotExists`. | Every minute |
| `QuotaExceeded` | A ResourceQuota rejected the init Job's pod. | Raise the quota or lower `spec.resources`. | Every 15 s |

The Services and the maintenance page already exist, except when the reason
is `InvalidSpec` or the "could not resolve…" form of
`DatabaseConnectionFailed`.

### Stuck in Initializing or Upgrading

The `<name>-init` or `<name>-upgrade-<hash>` Job is running; `Ready` says
"waiting for job …". Look at it:

```sh
kubectl -n <ns> get pods -l job-name=<job>
kubectl -n <ns> logs job/<job>
```

`ImagePullFailed` means the Job's pod cannot pull `spec.image`: push the
image, fix the pull secret, or change `spec.image` (the Job is then replaced).
A Job that runs past `spec.jobs.activeDeadlineSeconds` (one hour by default)
fails.

### Failed

| Cause | Reason | What to do |
|---|---|---|
| The init or upgrade Job failed | `InitJobFailed`, `UpgradeJobFailed`, `QuotaExceeded` | Read `kubectl -n <ns> logs job/<job>`, fix the cause, then **delete the Job** to retry: changing the spec alone does not. The failed Job stays until you delete it, so it is never retried behind your back. A failed upgrade leaves Odoo stopped; restoring a backup replaces the failed Job ([Restore and undo](../guides/restore.md)). |
| The database name changed | `DatabaseNameChanged` | `spec.database.name` (or the Secret value behind `nameFromSecret`) now resolves to another name than the instance uses. Put the old value back; the operator never switches databases. |
| The backup to start from was not restored | `BootstrapFailed` | The OdooRestore `<name>-bootstrap` of `spec.bootstrap.fromBackup` failed; the message has its reason, and Odoo does not start. Fix the cause (the file, the store, the image's Odoo series), then delete that OdooRestore: it is created again and retried, once the cleanup Job that deletes what the failed one left (its half-loaded copy) has run. See [Start an instance from a backup](../guides/start-from-a-backup.md#when-the-bootstrap-fails). When the message says the database already exists, the operator did not create it and never bootstraps over it: remove `spec.bootstrap`, see [An existing database](../guides/start-from-a-backup.md#an-existing-database). |
| A restore was deleted halfway through its swap | `RestoreIncomplete` | Odoo stays stopped. Check the `<db>_restore_*` / `<db>_prev_*` databases and filestore folders named in the restore Job's log, put the data right, then clear `status.currentOperation` on the OdooDeployment. |

### A new image is not rolled out

| What you see | Meaning |
|---|---|
| `Degraded` `ImagePullFailed`, the old image still serving | The `<name>-image-<hash>` check could not pull the new `spec.image`. Push it or fix the pull secret; retried every 15 s. |
| `Degraded` `ImageCheckFailed` | The image pulled but `<odooCommand> --version` failed. Read `kubectl logs job/<name>-image-<hash>`, fix the image, then delete that Job to check again. |
| `Degraded` `PreUpgradeBackupFailed`, the old image still serving | `spec.backup.beforeUpgrade` is `Required` and the backup before the upgrade failed; the message names it. Read the OdooBackup's `status.message`, fix the cause, then delete that OdooBackup to take it again. |
| OdooBackup `Failed`: "store … cannot be reached, so Odoo was not stopped" | The check before an offline or pre-upgrade backup could not write to the store, so Odoo kept serving. Read `kubectl -n <ns> logs job/<job>` (the message names it), fix the network, credentials or store, then take the backup again (for a pre-upgrade backup, delete the failed OdooBackup). |
| Phase `BackingUp`, Odoo stopped, before an upgrade | The backup `spec.backup.beforeUpgrade` takes before the upgrade runs with Odoo stopped; the upgrade starts when it ends. `status.preUpgradeBackup` names it. |
| Nothing happens | `spec.upgrade.onImageChange` is `false`: a new image is applied only when `spec.upgrade.token` changes. See [Upgrade Odoo](../guides/upgrades.md). |

### The maintenance page does not appear

It needs `spec.maintenancePage.enabled: true`, `spec.replicas` above 0, and an
Odoo Deployment with no available replica. A `MaintenancePageImageUnknown`
warning event means the operator could not read its own image from its pod;
set `--maintenance-page-image` ([Operator flags](../reference/operator-flags.md)).

### Other Degraded reasons

`PvcCreationFailed`, `OdooConfigSecretCreationFailed`,
`OdooAdminSecretCreationFailed`, `FailedCreateHttpService`,
`FailedCreatePollService`, `MaintenancePageFailed`, `DeploymentFailed`,
`JobCreationFailed` and `ReconcileFailed` mean a Kubernetes API call failed;
the message carries the API error (often RBAC, a quota or an invalid field).
They are retried with backoff. `BackupImageUnknown` means the operator could
not derive the backup image: set `--backup-image`. `DrillRunning` as a
`Degraded` reason means the drill Job's pod cannot start (an image pull, a
quota, scheduling, a missing Secret or a volume), and `JobCannotStart` the
same for a backup, restore, cleanup, init or upgrade Job: the message says
why. A backup, restore, drill or cleanup Job none of whose pods started a
container within 15 minutes is deleted and counted failed; `Deleting` and `DatabaseDropFailed` appear while the instance is
deleted (see below).

## Backups

| What you see | Meaning | What to do |
|---|---|---|
| `Pending`, "queued on …" | Waiting its turn: one backup, restore, drill or cleanup runs at a time per instance, and backups wait for the init Job, upgrades, a failed init or upgrade Job, and older backups. | Clear what is ahead; see the instance's `status.currentOperation`. |
| `Pending`, "waiting for OdooDeployment … which does not exist" | `spec.odooDeployment` names no instance in the namespace. | Fix the name. |
| `WaitingForFilestoreCleanup` | Odoo's attachment cleanup (`base.autovacuum_job`) runs or is due. | Nothing: retried every 2 minutes, and run anyway after 2 hours. |
| `Failed`, "has no spec.backup.stores" or a store or Secret error | Checked before the Job starts. | Fix the instance or store, then create a new OdooBackup. |
| `Failed`, "job … failed at step …" | The Job failed at that step (connect, dump, filestore, upload, verify…). | `kubectl -n <ns> logs job/<job>`; the Job is kept until its TTL. Create a new OdooBackup to retry; a scheduled backup tries again at its next time. |
| `Running`, "job … cannot start: …", instance `Degraded` `JobCannotStart` | The backup Job's pod cannot be scheduled, a container cannot be created (a missing Secret), its image cannot be pulled, or it waits long for a volume (see the same row under Restores). | Fix the cause. A Job none of whose pods started a container within 15 minutes is deleted and the backup fails ("started no container within 15m0s"); an offline backup then brings Odoo back. |
| `CompletedWithWarnings` | The backup is good, but some attachments were in neither the filestore nor the previous backup. | `status.missingFileNames` lists them. |
| `PartiallyCompleted` | The backup is in the first store, but a copy failed. | `status.copies` says why; fix the copy's store. |
| `Failed`, "keeps zip files and only takes copies" | The first store in `spec.backup.stores` is a zip store. | List a restic store first and the zip store after it. |
| `CompletedWithWarnings`, "could not delete …, whose backups retention forgot" | A zip store refused to delete the zip of a forgotten backup. | Check the store's credentials allow deleting; delete the named zips by hand. The next backup does not try again. |
| `CompletedWithWarnings`, "could not delete …, left by uploads that were cut short" | A zip store refused to delete a `.<zip>.partial` file a killed upload left. | Check the store's credentials allow deleting; the next copy tries again. |

A failed backup is final: the operator does not retry it. The
`BackupFailed`, `BackupCopyFailed` and `RetentionFailed` events, and the
example alerts, report failures.

## Restores

| What you see | Meaning | What to do |
|---|---|---|
| `Failed` with a `RestoreRefused` event | The restore was refused before it started: the backup does not exist, is of another instance, has not completed, the store is missing, the undo target is gone or not the latest. | Read `status.message`; create a corrected OdooRestore. |
| `Failed`, "the live data was not changed" | The Job failed before the swap. "no pod of it got past the steps before the swap" means its pods stopped in the load or Odoo step; the message names the step and the reason (`OOMKilled`, `ImagePullBackOff`). "the restore journal shows the swap never started" ends a restore that an attempt without a result had kept going; with "nothing to finish", that retry could only finish the swap, because the restore's source was gone. | `kubectl -n <ns> logs job/<job> -c <step>`; give the Job more memory (`spec.backup.resources`) or fix the image; nothing to undo. |
| `Failed`: "the backup was taken on Odoo … but the instance runs Odoo …" | A backup of another Odoo series than the instance's image (17.0 on 18.0). | Restore it on an image of its series, or upgrade it there first. |
| `Failed`: "no file in … matches" | `from.path`'s pattern matched no file in the store. | Check the folder and pattern; a pattern is only allowed in the last segment. |
| `Failed`: "has no encryptionKeySecret" | `from` names a restic repository in a store without a key. | Add the repository's key to the store, or point `from.path` at a `.zip` or `.dump` file. |
| `Failed`: "neutralize" | Odoo could not neutralize the loaded database (Odoo before 16 has no `neutralize`). | Read the Job's `odoo` container log; restore without `neutralize` and neutralize by hand. |
| `Failed` after one pod, the Job's condition `PodFailurePolicy` | The restore was refused by a check no new pod can change (the message names it), so the Job ended at its first pod (exit code 3). | Fix the cause the message names; create a new OdooRestore. |
| `Restoring` for long, instance `Degraded` `RestoreIncomplete` | The Job failed after the swap may have started: its result said so, or its main container started and left no result (`status.liveChangeUnconfirmed`). It is retried, from 30 seconds up to every 15 minutes, until it finishes, or until an attempt reports from the restore journal that the swap never started. Odoo stays stopped meanwhile. A retry after the swap started only finishes it from the journal (`--finish-swap`), so it needs neither the backup nor the store. | Fix the cause (usually the database or the data volume); it carries on by itself. |
| `Stopping` | Waiting for Odoo's pods to stop. | — |
| `Restoring`, "job … cannot start: …", instance `Degraded` `JobCannotStart` | The restore Job's pod cannot start: it cannot be scheduled (`Unschedulable`, with the scheduler's message), a container cannot be created (`CreateContainerConfigError`: a Secret or ConfigMap it names is missing), its image cannot be pulled, or it has waited more than 5 minutes for its first container after it was scheduled (most often a `ReadWriteOnce` data volume still attached to another node, or a volume that fails to mount; `kubectl describe pod` shows the events). Odoo is stopped meanwhile. | Fix the cause. If no pod of the Job starts a container within 15 minutes, the operator deletes the Job and the restore fails with the live data untouched ("started no container within 15m0s"), and Odoo comes back. |

## Backup stores

| Reason in `kubectl get odoobackupstore` | Meaning | Retried |
|---|---|---|
| `InvalidSpec` | The store's spec cannot be used. | On the spec change |
| `SecretMissing` | A Secret or a key in it is missing. | Every minute, and when the Secret appears |
| `Probing` | The probe Job runs; "cannot start" means it cannot pull the backup image. | Every 10 s; the probe fails after 5 minutes |
| `StoreUnreachable` | The probe could not write, read or delete a file. The message has the `kubectl logs` command for the probe Job. | Every 2 minutes |
| `StoreReachable` | Ready. | Probed again every 6 hours |

A `Schedules` condition that is `False` (`InvalidSchedule`) means
`spec.check.schedule` or `spec.prune.schedule` does not parse. Checks and
prunes run only while the store is ready; a failed one keeps its Job (named
in `status.lastCheck.job` or `status.lastPrune.job`) for a day, its TTL, or
until the next run if that comes sooner.

## Restore drills

`status.backup.lastDrill` has the outcome. A failed drill keeps its Job,
named in `status.backup.lastDrill.job`; its log spans several containers:

```sh
kubectl -n <ns> logs job/<job> --all-containers
```

## Deleting an instance hangs

Deletion waits for the instance's pods to stop, including running backup,
restore, drill and cleanup Jobs (up to their deadline), so that none is
killed half way through: a restore cut short could leave the database
swapped and the filestore not. With `spec.database.deletionPolicy: Delete`
the finalizer `odoo.abugharbia.com/database` holds the instance, the init or
upgrade Job is deleted (its database is dropped anyway) and the database is
dropped afterwards. Otherwise (`Retain`, or a database the operator did not
create) the finalizer `odoo.abugharbia.com/operations` holds it only while
one of its Jobs runs, init and upgrade Jobs included, and is removed as soon
as none does. A Job none of whose pods ever started a container (an image
that cannot be pulled) is deleted rather than waited for, and so is the
image check of a new `spec.image` (the Job `<name>-image-<hash>`, see
[A new image is not rolled out](#a-new-image-is-not-rolled-out)), stuck
pulling or not, since it mounts nothing and changes nothing. The `Ready`
condition says how many pods are still awaited. A
`DatabaseDropFailed` event means the drop keeps failing (retried every 30
seconds): fix the database access, or set `deletionPolicy: Retain` to skip
the drop. A database the operator did not create is never dropped. A
`DatabaseDropRefused` event means the database no longer carries the
operator's owner tag; `DatabaseNotDropped` that the connection could not be
resolved, so the operator could not drop it.

With the database dropped and the filestore volume kept, the deletion also
runs a cleanup Job per restore that left folders on the volume, one after the
other, before it drops the database; the `Ready` condition names the Job. One
that fails, or exceeds its one-hour deadline, does not hold the deletion: a
`RestoreFilesKept` event and the `odoo.abugharbia.com/files-kept` annotation
on that OdooRestore name the paths left on the volume, to delete by hand.

An OdooRestore stays until its cleanup has deleted the data it kept, or until
its instance's deletion has dealt with it. Backups and restores of an
instance being deleted are left to that deletion: a backup whose Job
finishes meanwhile records its snapshot, and a restore it cut short is
`Failed` with "was deleted while the restore was running".

## Reading a Job's result

Backup, restore, probe and store Jobs write their result as JSON into the
termination message of their `backup` container, besides the log. The
operator reads the newest pod of the Job only (a retried Job has one pod
per attempt); when its `backup` container did not finish, the Job has no
result and the message is taken from the pod's statuses:

```sh
kubectl -n <ns> get pods -l job-name=<job> --sort-by=.metadata.creationTimestamp \
  -o jsonpath='{.items[-1:].status.containerStatuses[?(@.name=="backup")].state.terminated.message}'
```

The `backup` container exits 0 when the Job did what it was asked, 1 when it
failed in a way a new pod may get past (the Job's `backoffLimit` retries
it), 2 for a command line it does not take, and 3 for a failure no new pod
can change (`"final": true` in the result, such as a restore whose backup
does not pass its checks). The Job's pod failure policy ends the Job at once
on 2 and 3, so the Job's condition then reads `PodFailurePolicy` instead of
`BackoffLimitExceeded`.

A result that does not fit in the 4096 bytes Kubernetes keeps loses its
warnings and list entries first (they are also in the log). A restore's
`missingModules` is never shortened: when it does not fit, the result has
`"missingModulesUnknown": true` instead, and the operator reads the modules
installed in the restored database itself. A restore that stays `Restoring`
after its Job completed, with a reconcile error "reading them from it
failed", is waiting for that database read.
