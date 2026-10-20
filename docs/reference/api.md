# API Reference

## Packages
- [odoo.abugharbia.com/v1](#odooabugharbiacomv1)


## odoo.abugharbia.com/v1

Package v1 contains API Schema definitions for the odoo v1 API group

### Resource Types
- [OdooBackup](#odoobackup)
- [OdooBackupStore](#odoobackupstore)
- [OdooDeployment](#odoodeployment)
- [OdooRestore](#odoorestore)



#### BackupConsistency

_Underlying type:_ _string_

BackupConsistency is how a backup keeps the database and the filestore
in step.

_Validation:_
- Enum: [Online Offline]

_Appears in:_
- [OdooBackupConfig](#odoobackupconfig)

| Field | Description |
| --- | --- |
| `Online` | BackupConsistencyOnline backs up while Odoo keeps serving: the dump and<br />the list of files come from one database snapshot, and files deleted<br />meanwhile are taken from the previous backup.<br /> |
| `Offline` | BackupConsistencyOffline stops Odoo for the backup.<br /> |


#### BackupCopy



BackupCopy is one copy of a backup in another store.



_Appears in:_
- [OdooBackupStatus](#odoobackupstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `store` _string_ | Store is the OdooBackupStore holding the copy. |  |  |
| `snapshotID` _string_ | SnapshotID of the copy in that store's repository; empty when the<br />copy failed. |  |  |
| `file` _string_ | File is the copy in a store of format zip: the Odoo zip's path below<br />the store's root. |  |  |
| `error` _string_ | Error says why the copy failed. |  |  |


#### BackupSource



BackupSource is a backup that is not one of the instance's own
OdooBackups: an Odoo backup file (.zip or .dump, as Odoo's database
manager makes them) or a snapshot in any restic repository, in an
OdooBackupStore of the same namespace.



_Appears in:_
- [BootstrapFromBackup](#bootstrapfrombackup)
- [OdooRestoreSpec](#odoorestorespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `store` _string_ | Store is the OdooBackupStore that holds the backup. |  | MaxLength: 253 <br />MinLength: 1 <br /> |
| `path` _string_ | Path inside the store, below its path (or subPath). A path ending in<br />.zip or .dump is an Odoo backup file; its last segment may be a<br />pattern (*, ?, [a-z]), and the match whose name sorts last, the<br />newest for dated names, is restored. Any other path is a restic<br />repository folder; empty is this instance's own repository<br />(&lt;namespace>/&lt;name>). |  | MaxLength: 512 <br />Pattern: `^([A-Za-z0-9._~+@=-]+/)*[A-Za-z0-9._~+@=*?\[\]-]+$` <br /> |
| `snapshot` _string_ | Snapshot in a restic repository: its ID, or latest (default) for the<br />newest snapshot with Host and Tags. |  | Pattern: `^(latest\|[0-9a-f]\{8,64\})$` <br /> |
| `host` _string_ | Host picks latest among the snapshots of one restic --host, such as<br />&lt;namespace>/&lt;name> for the operator's own snapshots. |  | MaxLength: 253 <br /> |
| `tags` _string array_ | Tags picks latest among the snapshots that have all these restic<br />tags, such as deployment=&lt;namespace>/&lt;name>. |  | MaxItems: 10 <br />items:MaxLength: 253 <br /> |


#### BackupStoreFormat

_Underlying type:_ _string_

BackupStoreFormat is how backups are kept in a store.

_Validation:_
- Enum: [restic zip]

_Appears in:_
- [OdooBackupStoreSpec](#odoobackupstorespec)

| Field | Description |
| --- | --- |
| `restic` | BackupStoreFormatRestic keeps one encrypted, deduplicated restic<br />repository per OdooDeployment.<br /> |
| `zip` | BackupStoreFormatZip keeps every backup as the zip Odoo's database<br />manager makes, unencrypted and not deduplicated, in one folder per<br />OdooDeployment. A zip store only receives copies.<br /> |


#### BeforeUpgrade

_Underlying type:_ _string_

BeforeUpgrade says whether a backup is taken before an upgrade.

_Validation:_
- Enum: [Off BestEffort Required]

_Appears in:_
- [OdooBackupConfig](#odoobackupconfig)

| Field | Description |
| --- | --- |
| `Off` | BeforeUpgradeOff upgrades without a backup first.<br /> |
| `BestEffort` | BeforeUpgradeBestEffort takes a backup first and upgrades even when<br />it fails.<br /> |
| `Required` | BeforeUpgradeRequired upgrades only after the backup succeeded; a<br />failed backup keeps the running image.<br /> |


#### Bootstrap



Bootstrap says how an instance's database is made on first boot.



_Appears in:_
- [OdooDeploymentSpec](#odoodeploymentspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `fromBackup` _[BootstrapFromBackup](#bootstrapfrombackup)_ | FromBackup restores a backup instead of initializing a new database:<br />the way an existing Odoo moves onto the operator, or an instance is<br />recreated from its backups. It is used once, before Odoo first<br />starts; later changes are ignored. |  | Optional: \{\} <br /> |


#### BootstrapFromBackup



BootstrapFromBackup is the backup an instance starts from.



_Appears in:_
- [Bootstrap](#bootstrap)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `store` _string_ | Store is the OdooBackupStore that holds the backup. |  | MaxLength: 253 <br />MinLength: 1 <br /> |
| `path` _string_ | Path inside the store, below its path (or subPath). A path ending in<br />.zip or .dump is an Odoo backup file; its last segment may be a<br />pattern (*, ?, [a-z]), and the match whose name sorts last, the<br />newest for dated names, is restored. Any other path is a restic<br />repository folder; empty is this instance's own repository<br />(&lt;namespace>/&lt;name>). |  | MaxLength: 512 <br />Pattern: `^([A-Za-z0-9._~+@=-]+/)*[A-Za-z0-9._~+@=*?\[\]-]+$` <br /> |
| `snapshot` _string_ | Snapshot in a restic repository: its ID, or latest (default) for the<br />newest snapshot with Host and Tags. |  | Pattern: `^(latest\|[0-9a-f]\{8,64\})$` <br /> |
| `host` _string_ | Host picks latest among the snapshots of one restic --host, such as<br />&lt;namespace>/&lt;name> for the operator's own snapshots. |  | MaxLength: 253 <br /> |
| `tags` _string array_ | Tags picks latest among the snapshots that have all these restic<br />tags, such as deployment=&lt;namespace>/&lt;name>. |  | MaxItems: 10 <br />items:MaxLength: 253 <br /> |
| `neutralize` _boolean_ | Neutralize runs Odoo's neutralize on the restored database before<br />Odoo first starts on it, and gives it a new database.uuid (see<br />OdooRestore spec.neutralize). Set it for a copy of production. |  |  |


#### BootstrapStatus



BootstrapStatus records the restore spec.bootstrap.fromBackup started.



_Appears in:_
- [OdooDeploymentStatus](#odoodeploymentstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `restore` _string_ | Restore is the OdooRestore that restores the backup. |  |  |
| `completed` _boolean_ | Completed is set once it has restored the backup. |  |  |




#### DatabaseCreatePolicy

_Underlying type:_ _string_

DatabaseCreatePolicy tells the operator what to do when the database named in
spec.database does not exist.

_Validation:_
- Enum: [IfNotExists Never]

_Appears in:_
- [OdooDatabaseConfig](#odoodatabaseconfig)

| Field | Description |
| --- | --- |
| `IfNotExists` | DatabaseCreatePolicyIfNotExists creates the database when it is missing and<br />adopts it as an external database when it already exists.<br /> |
| `Never` | DatabaseCreatePolicyNever never creates a database; a missing database is reported as Degraded.<br /> |


#### DeletionPolicy

_Underlying type:_ _string_

DeletionPolicy tells the operator what to do with an owned resource when the
OdooDeployment is deleted.

_Validation:_
- Enum: [Retain Delete]

_Appears in:_
- [OdooDatabaseConfig](#odoodatabaseconfig)
- [PersistentVolumeClaimSpec](#persistentvolumeclaimspec)

| Field | Description |
| --- | --- |
| `Retain` |  |
| `Delete` |  |


#### MaintenanceJobStatus



MaintenanceJobStatus records the maintenance Job (init or upgrade) the
operator is waiting for. The JSON keys keep the 0.2.x names.



_Appears in:_
- [OdooDeploymentStatus](#odoodeploymentstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | The name of the Job |  |  |
| `jobNamespace` _string_ | The namespace of the Job |  |  |
| `kind` _string_ | Kind is "init" or "upgrade" |  |  |
| `image` _string_ | Image the Job runs; becomes status.appliedImage on success |  |  |
| `token` _string_ | Token is the spec.upgrade.token the Job was created for; becomes status.appliedUpgradeToken on success |  |  |
| `modules` _string array_ | The list of modules that are being installed (-i) |  |  |
| `upgradeModules` _string array_ | The list of modules that are being upgraded (-u) |  |  |
| `startedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | StartedAt is when the operator created the Job. |  |  |


#### MaintenancePageConfig



MaintenancePageConfig controls the page the http Service serves while Odoo
itself cannot answer: during the init and upgrade Jobs and until the
Deployment has an available replica again.



_Appears in:_
- [OdooDeploymentSpec](#odoodeploymentspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled runs a small &lt;name>-maintenance Deployment from the operator's<br />own image and points the http Service at it whenever Odoo has no<br />available replica (and spec.replicas is not 0). The poll Service always<br />stays on Odoo. | false |  |
| `title` _[MaintenancePageText](#maintenancepagetext)_ | Title is the product name the page shows ("Updating &lt;title>"). | Odoo | MaxLength: 64 <br />MinLength: 1 <br />Pattern: `^[^\n\r<>]*$` <br /> |
| `titleTranslations` _object (keys:string, values:[MaintenancePageText](#maintenancepagetext))_ | TitleTranslations replaces the title for a page language. The page is<br />shown in English or Arabic, picked from the browser's Accept-Language,<br />so the only key it reads today is "ar". |  | MaxProperties: 8 <br /> |
| `accentColor` _string_ | AccentColor is the page's accent, as #rrggbb. | #714b67 | Pattern: `^#[0-9A-Fa-f]\{6\}$` <br /> |
| `logo` _string_ | Logo is shown instead of the title's initial. It must be a base64 data<br />URI: Odoo is not answering while the page is up, so the page cannot<br />load images from it. |  | MaxLength: 65536 <br />Pattern: `^data:image/(png\|jpeg\|gif\|webp\|svg\+xml);base64,[A-Za-z0-9+/]+=*$` <br /> |


#### MaintenancePageText

_Underlying type:_ _string_

MaintenancePageText is one line of text shown on the maintenance page.

_Validation:_
- MaxLength: 64
- MinLength: 1
- Pattern: `^[^\n\r<>]*$`

_Appears in:_
- [MaintenancePageConfig](#maintenancepageconfig)



#### NFSStore



NFSStore is an NFS export, mounted into the backup Jobs. The nodes need an
NFS client and a route to the server.



_Appears in:_
- [OdooBackupStoreSpec](#odoobackupstorespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `server` _string_ | Server is the NFS server's host name or address. |  | MinLength: 1 <br /> |
| `path` _string_ | Path is the exported folder. Repositories go under &lt;path>/&lt;namespace>/&lt;deployment>/. |  | Pattern: `^/` <br /> |


#### OdooBackup



OdooBackup is one full backup of an OdooDeployment: its database and its
filestore, taken together. Create one to take a backup now. It is not owned
by the OdooDeployment, so deleting an instance never deletes its backups.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `odoo.abugharbia.com/v1` | | |
| `kind` _string_ | `OdooBackup` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[OdooBackupSpec](#odoobackupspec)_ |  |  |  |
| `status` _[OdooBackupStatus](#odoobackupstatus)_ |  |  |  |


#### OdooBackupConfig



OdooBackupConfig turns on backups of this instance.



_Appears in:_
- [OdooDeploymentSpec](#odoodeploymentspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `stores` _string array_ | Stores are OdooBackupStores in this namespace. Backups are written to<br />the first; the instance's repository is &lt;path>/&lt;namespace>/&lt;name>/<br />inside it. Each backup is then copied to the other stores. |  | MaxItems: 4 <br />items:MinLength: 1 <br /> |
| `schedule` _string_ | Schedule takes a backup on a cron schedule ("0 3 * * *" or @daily), in<br />TimeZone. Empty (the default) takes no scheduled backups. A schedule<br />missed while the operator was down runs once when it is back. |  | MaxLength: 100 <br /> |
| `timeZone` _string_ | TimeZone of Schedule, the drill schedule and the retention rules, as an<br />IANA name such as Europe/Berlin. | UTC | MaxLength: 100 <br /> |
| `retention` _[OdooBackupRetention](#odoobackupretention)_ | Retention says which backups are kept after each backup. | \{  \} |  |
| `drill` _[OdooBackupDrill](#odoobackupdrill)_ | Drill restores the newest backup on a schedule to prove it works. |  | Optional: \{\} <br /> |
| `beforeUpgrade` _[BeforeUpgrade](#beforeupgrade)_ | BeforeUpgrade takes a backup before each upgrade (a new spec.image or<br />spec.upgrade.token), with Odoo stopped so the backup holds the data<br />exactly as the upgrade found it. Off (default) upgrades without one;<br />BestEffort upgrades even when the backup fails; Required keeps the<br />running image when it fails, until the failed OdooBackup is deleted<br />(which tries again) or the upgrade target changes. | Off | Enum: [Off BestEffort Required] <br /> |
| `consistency` _[BackupConsistency](#backupconsistency)_ | Consistency: Online (default) backs up while Odoo keeps serving;<br />Offline stops Odoo for the backup. | Online | Enum: [Online Offline] <br /> |
| `restore` _[OdooRestoreConfig](#odoorestoreconfig)_ | Restore holds the defaults of in-place restores (OdooRestore). | \{  \} |  |
| `resources` _[ResourceRequirements](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#resourcerequirements-v1-core)_ | Resources of the backup and restore Jobs. |  | Optional: \{\} <br /> |
| `activeDeadlineSeconds` _integer_ | ActiveDeadlineSeconds bounds one backup or restore Job. | 21600 | Minimum: 60 <br /> |


#### OdooBackupDrill



OdooBackupDrill restores the newest backup on a schedule to prove it can
be restored, without touching the running instance.



_Appears in:_
- [OdooBackupConfig](#odoobackupconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `schedule` _string_ | Schedule in cron format, in spec.backup.timeZone; empty turns drills off. |  | MaxLength: 100 <br /> |
| `scratchSizeLimit` _[Quantity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#quantity-resource-api)_ | ScratchSizeLimit caps the node disk the drill's scratch filestore<br />(an emptyDir) may use; unset leaves it uncapped. A drill that needs<br />more is evicted and reported failed. |  | Optional: \{\} <br /> |


#### OdooBackupRetention



OdooBackupRetention says which backups are kept. Scheduled backups are
thinned with restic's keep rules (daily: 7 keeps the newest backup of each
of the last 7 days that have one; a backup kept by any rule stays); manual
backups keep their newest Manual and pre-upgrade backups their newest
PreUpgrade. A backup with
spec.keep, and the newest backup, are never deleted. Unset fields take the
defaults below.



_Appears in:_
- [OdooBackupConfig](#odoobackupconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `last` _integer_ | Last keeps the newest N backups. |  | Minimum: 0 <br /> |
| `hourly` _integer_ | Hourly keeps the newest backup of each of the last N hours that have one. |  | Minimum: 0 <br /> |
| `daily` _integer_ | Daily keeps the newest backup of each of the last N days that have one. | 7 | Minimum: 0 <br /> |
| `weekly` _integer_ | Weekly keeps the newest backup of each of the last N weeks that have one. | 4 | Minimum: 0 <br /> |
| `monthly` _integer_ | Monthly keeps the newest backup of each of the last N months that have one. | 12 | Minimum: 0 <br /> |
| `yearly` _integer_ | Yearly keeps the newest backup of each of the last N years that have one. |  | Minimum: 0 <br /> |
| `within` _string_ | Within keeps every backup newer than this, counted from the newest<br />backup, such as 30d, 12h or 1y6m (restic's duration format). |  | Pattern: `^([0-9]+[ymdh])*$` <br /> |
| `manual` _integer_ | Manual keeps the newest N manual backups; 0 keeps them all. |  | Minimum: 0 <br /> |
| `preUpgrade` _integer_ | PreUpgrade keeps the newest N backups taken before upgrades; 0 keeps<br />them all. | 5 | Minimum: 0 <br /> |
| `keepAll` _boolean_ | KeepAll turns retention off: no backup is ever deleted. |  |  |


#### OdooBackupScheduleStatus



OdooBackupScheduleStatus reports the backup schedule and the last drill.



_Appears in:_
- [OdooDeploymentStatus](#odoodeploymentstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `lastScheduleTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | LastScheduleTime is the latest scheduled time the operator has handled,<br />whether it took a backup then or skipped it. |  |  |
| `nextScheduleTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | NextScheduleTime is when the next scheduled backup is due. |  |  |
| `schedule` _string_ | Schedule and TimeZone the times above were computed for. |  |  |
| `timeZone` _string_ |  |  |  |
| `lastDrillScheduleTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | LastDrillScheduleTime is the latest drill time the operator has handled. |  |  |
| `nextDrillTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | NextDrillTime is when the next drill is due. |  |  |
| `drillSchedule` _string_ | DrillSchedule the drill times above were computed for. |  |  |
| `drillDue` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | DrillDue is the scheduled time of a drill waiting in the instance's<br />queue. |  |  |
| `lastDrill` _[OdooDrillResult](#odoodrillresult)_ | LastDrill is the result of the latest restore drill. |  |  |
| `lastSuccessfulDrillTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | LastSuccessfulDrillTime is when a restore drill last passed. |  |  |


#### OdooBackupSpec



OdooBackupSpec names the instance to back up.



_Appears in:_
- [OdooBackup](#odoobackup)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `odooDeployment` _string_ | OdooDeployment to back up, in the same namespace. |  | MaxLength: 253 <br />MinLength: 1 <br /> |
| `keep` _boolean_ | Keep marks a backup that retention never removes. |  | Optional: \{\} <br /> |


#### OdooBackupStatus



OdooBackupStatus describes one backup.



_Appears in:_
- [OdooBackup](#odoobackup)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _string_ | Phase: Pending, WaitingForFilestoreCleanup, Running, Completed,<br />CompletedWithWarnings, PartiallyCompleted (taken, but a copy to another<br />store failed) or Failed. |  | Enum: [Pending WaitingForFilestoreCleanup Running Completed CompletedWithWarnings PartiallyCompleted Failed] <br /> |
| `message` _string_ | Message explains the phase in a sentence. |  |  |
| `store` _string_ | Store is the OdooBackupStore the backup was written to. |  |  |
| `repository` _string_ | Repository is the restic repository inside the store, &lt;namespace>/&lt;deployment>. |  |  |
| `snapshotID` _string_ | SnapshotID is the restic snapshot holding this backup. |  |  |
| `consistency` _string_ | Consistency the backup was taken with: Online or Offline. |  |  |
| `job` _string_ | Job is the Job taking the backup. Attempts counts the Jobs started for it. |  |  |
| `attempts` _integer_ |  |  |  |
| `retryAfter` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | RetryAfter is when a backup that waited for Odoo's filestore cleanup<br />is tried again. |  |  |
| `waitingSince` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | WaitingSince is when the backup started waiting for Odoo's filestore<br />cleanup to finish. |  |  |
| `startedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | StartedAt is when the backup Job was created. |  |  |
| `completedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | CompletedAt is when the backup finished, successfully or not. |  |  |
| `image` _string_ | Image is the Odoo image the instance ran when the backup was taken. |  |  |
| `odooVersion` _string_ | OdooVersion is the Odoo series in the database, such as 18.0. |  |  |
| `pgServerVersion` _string_ | PgServerVersion is the PostgreSQL server version, such as 16.4. |  |  |
| `databaseBytes` _integer_ | DatabaseBytes is the size of the SQL dump. |  |  |
| `filestoreBytes` _integer_ | FilestoreBytes is the size of the filestore. |  |  |
| `filestoreFiles` _integer_ | FilestoreFiles is the number of files in the filestore. |  |  |
| `addedBytes` _integer_ | AddedBytes is the new data this backup uploaded after deduplication<br />and compression. |  |  |
| `repairedFiles` _integer_ | RepairedFiles counts attachments that were gone from the filestore<br />when the backup ran and were taken from the previous backup instead. |  |  |
| `missingFiles` _integer_ | MissingFiles counts attachments the database refers to that are in<br />neither the filestore nor the previous backup. |  |  |
| `missingFileNames` _string array_ | MissingFileNames lists the first missing files. |  |  |
| `copies` _[BackupCopy](#backupcopy) array_ | Copies are the copies of this backup in the instance's other stores. |  |  |
| `warnings` _string array_ | Warnings name what went wrong around a backup that was still taken,<br />such as retention that could not run. |  |  |


#### OdooBackupStore



OdooBackupStore is a place backups are kept: an S3 bucket, an SFTP server,
an NFS export or a PersistentVolumeClaim. OdooDeployments in the same
namespace name it in spec.backup.stores.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `odoo.abugharbia.com/v1` | | |
| `kind` _string_ | `OdooBackupStore` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[OdooBackupStoreSpec](#odoobackupstorespec)_ |  |  |  |
| `status` _[OdooBackupStoreStatus](#odoobackupstorestatus)_ |  |  |  |


#### OdooBackupStoreSpec



OdooBackupStoreSpec is where backups are written to and read from.



_Appears in:_
- [OdooBackupStore](#odoobackupstore)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `format` _[BackupStoreFormat](#backupstoreformat)_ | Format is how backups are kept. restic: one encrypted, deduplicated<br />repository per OdooDeployment at &lt;path>/&lt;namespace>/&lt;deployment>/.<br />zip: every backup as the zip Odoo's database manager makes, at<br />&lt;path>/&lt;namespace>/&lt;deployment>/&lt;db>_&lt;YYYY-MM-DD_HH-MM-SS>.zip (UTC),<br />unencrypted; a zip store can only be a copy store (not the first in<br />spec.backup.stores) and needs no encryptionKeySecret. | restic | Enum: [restic zip] <br /> |
| `s3` _[S3Store](#s3store)_ | S3 stores backups in an S3 bucket. |  |  |
| `sftp` _[SFTPStore](#sftpstore)_ | SFTP stores backups on an SSH server. |  |  |
| `nfs` _[NFSStore](#nfsstore)_ | NFS stores backups on an NFS export. |  |  |
| `pvc` _[PVCStore](#pvcstore)_ | PVC stores backups on a PersistentVolumeClaim. |  |  |
| `encryptionKeySecret` _[SecretKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#secretkeyselector-v1-core)_ | EncryptionKeySecret is the restic password that encrypts every<br />backup in the store. Without it no backup can be read: keep a copy<br />outside the cluster. A readOnly store that only holds Odoo zip or<br />.dump files, and a store of format zip, do not need one. |  | Optional: \{\} <br /> |
| `readOnly` _boolean_ | ReadOnly stores are only read from: OdooRestores and<br />spec.bootstrap.fromBackup take backups from them, but no backup or<br />copy is ever written there, they are never checked or pruned, and<br />their probe only lists the store. Read-only credentials are enough,<br />and restic reads the repositories without locking them. |  |  |
| `check` _[StoreCheck](#storecheck)_ | Check reads the store's repositories back on a schedule (restic check)<br />to find damage before a restore needs them. |  | Optional: \{\} <br /> |
| `prune` _[StoreSchedule](#storeschedule)_ | Prune frees the space of the backups retention has removed, on a<br />schedule (restic prune). |  | Optional: \{\} <br /> |
| `timeZone` _string_ | TimeZone of the check and prune schedules, as an IANA name. | UTC | MaxLength: 100 <br /> |
| `resources` _[ResourceRequirements](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#resourcerequirements-v1-core)_ | Resources of the Jobs that probe, check and prune the store. |  | Optional: \{\} <br /> |


#### OdooBackupStoreStatus



OdooBackupStoreStatus reports whether the store can be reached.



_Appears in:_
- [OdooBackupStore](#odoobackupstore)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the generation the status describes. |  |  |
| `ready` _boolean_ | Ready is true when the last probe could write to, read from and<br />delete a file in the store (list it, for a readOnly store). |  | Optional: \{\} <br /> |
| `lastProbeTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | LastProbeTime is when the store was last probed. |  |  |
| `probeJob` _string_ | ProbeJob is the Job probing the store right now. |  |  |
| `probedHash` _string_ | ProbedHash identifies the spec and Secrets the last probe ran with. |  |  |
| `taskJob` _string_ | TaskJob is the check or prune Job running right now; TaskKind says which. |  |  |
| `taskKind` _string_ |  |  |  |
| `lastCheck` _[StoreTaskStatus](#storetaskstatus)_ | LastCheck and LastPrune are the outcomes of the latest check and prune. |  |  |
| `lastPrune` _[StoreTaskStatus](#storetaskstatus)_ |  |  |  |
| `lastCheckScheduleTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | LastCheckScheduleTime and LastPruneScheduleTime are the latest<br />scheduled times handled; NextCheckTime and NextPruneTime the next ones. |  |  |
| `lastPruneScheduleTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ |  |  |  |
| `nextCheckTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ |  |  |  |
| `nextPruneTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ |  |  |  |
| `checkRuns` _integer_ | CheckRuns counts checks, to pick the part of the data the next reads. |  |  |
| `scheduleKey` _string_ | Schedules the times above were computed for: check, prune and time zone. |  |  |
| `repositories` _integer_ | Repositories is how many instance repositories the latest task saw,<br />StoredBytes how much they hold together. |  |  |
| `storedBytes` _integer_ |  |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#condition-v1-meta) array_ | Conditions: Ready. |  |  |


#### OdooConfig







_Appears in:_
- [OdooDeploymentSpec](#odoodeploymentspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `adminPasswordSecretName` _string_ | The admin password to use for the Odoo application<br />The admin passowrd is used to create/copy or delete odoo databases<br />This can be left empty to generate a secure random password |  |  |
| `debugMode` _boolean_ | Enable debug mode for Odoo | false |  |
| `dataDir` _string_ | The directory to use for the odoo filestore and session store | /var/lib/odoo |  |
| `withoutDemo` _boolean_ | Install modules without demo data | true |  |
| `proxyMode` _boolean_ | Proxy Mode for Odoo<br />This instructs Odoo to use the X-Forwarded-For header for the remote IP address | true |  |
| `workers` _integer_ | Numer of process workers to use for Odoo. 0 runs the threaded server. | 2 | Minimum: 0 <br /> |
| `limitRequest` _integer_ | The maximum number of requests that the process can take | 8192 |  |
| `limitTimeReal` _integer_ | The maximum real time in seconds that the process can take | 120 |  |
| `limitTimeCpu` _integer_ | The maximum CPU time in seconds that the process can take | 60 |  |
| `limitMemorySoft` _integer_ | The maximum memory in bytes that the process can take | 2147483648 |  |
| `limitMemoryHard` _integer_ | The maximum memory in bytes that the process can take | 2684354560 |  |
| `maxCronThreads` _integer_ | The maximum number of cron threads to use for Odoo | 1 | Minimum: 0 <br /> |
| `extraAddonsPaths` _string array_ | Extra addons paths for Odoo. Each entry must be an absolute path with no commas, spaces, newlines, or # characters. |  | Optional: \{\} <br />items:Pattern: `^/[^,\n\r# ]+$` <br /> |
| `listDb` _boolean_ | ListDb controls the database manager. When false (the default) the<br />rendered odoo.conf carries list_db = False and dbfilter = ^&lt;db_name>$ so<br />the instance can only ever see its own database. | false |  |
| `dbFilter` _string_ | DbFilter is written verbatim as dbfilter and replaces the derived<br />^&lt;db_name>$ filter. Single line. |  | Pattern: `^[^\n\r]*$` <br /> |
| `serverWideModules` _string array_ | ServerWideModules is rendered as server_wide_modules (Odoo's default is base,web). |  | items:Pattern: `^[A-Za-z0-9_]+$` <br /> |
| `loadLanguages` _string array_ | LoadLanguages is passed as --load-language to maintenance Jobs that<br />install modules (-i), so translations are loaded at first boot. |  | items:Pattern: `^[a-z]\{2,3\}(_[A-Za-z0-9]\{2,4\})?$` <br /> |
| `extraOptions` _object (keys:string, values:[OdooOptionValue](#odoooptionvalue))_ | ExtraOptions are appended verbatim to the [options] section, sorted by<br />key. Keys must be odoo.conf option names (^[a-z_][a-z0-9_]*$, checked by<br />the operator, which reports Degraded/InvalidSpec otherwise); values must<br />be single line (enforced by the schema). |  | MaxProperties: 64 <br /> |


#### OdooDatabaseConfig



OdooDatabaseConfig defines the database connection configuration for Odoo



_Appears in:_
- [OdooDeploymentSpec](#odoodeploymentspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `host` _string_ | The database host to use for Odoo | postgresql |  |
| `hostFromSecret` _[SecretKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#secretkeyselector-v1-core)_ | The database host to use for Odoo from a secret |  |  |
| `port` _integer_ | The database port to use for Odoo | 5432 |  |
| `portFromSecret` _[SecretKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#secretkeyselector-v1-core)_ | The database port to use for Odoo from a secret |  |  |
| `user` _string_ | The database user to use for Odoo | odoo |  |
| `userFromSecret` _[SecretKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#secretkeyselector-v1-core)_ | The database user to use for Odoo from a secret |  |  |
| `passwordFromSecret` _[SecretKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#secretkeyselector-v1-core)_ | The database password to use for Odoo |  |  |
| `name` _string_ | The database name to use for Odoo. Immutable: the operator only ever<br />drops the database it recorded in status.database, so renaming would<br />silently orphan it. | odoo | MaxLength: 63 <br />Pattern: `^[A-Za-z_][A-Za-z0-9_-]*$` <br /> |
| `nameFromSecret` _[SecretKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#secretkeyselector-v1-core)_ | The database name to use for Odoo from a secret. Immutable for the same reason as name. |  |  |
| `ssl` _boolean_ | Whether or not to enable SSL for the database connection.<br />Rendered as db_sslmode = require (true) or disable (false). | false |  |
| `sslFromSecret` _[SecretKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#secretkeyselector-v1-core)_ | Whether or not to enable SSL for the database connection from a secret |  |  |
| `maxConn` _integer_ | The database max connections to use for Odoo | 20 |  |
| `maxConnFromSecret` _[SecretKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#secretkeyselector-v1-core)_ | The database max connections to use for Odoo from a secret |  |  |
| `createPolicy` _[DatabaseCreatePolicy](#databasecreatepolicy)_ | CreatePolicy controls whether the operator creates the database when it<br />does not exist. A database that already exists is adopted as "external"<br />and is never dropped by the operator. | IfNotExists | Enum: [IfNotExists Never] <br /> |
| `deletionPolicy` _[DeletionPolicy](#deletionpolicy)_ | DeletionPolicy controls whether a database the operator created is<br />dropped when the OdooDeployment is deleted. Databases the operator did<br />not create (status.database.provisionedBy: external) are always retained. | Retain | Enum: [Retain Delete] <br /> |
| `maintenanceDatabase` _string_ | MaintenanceDatabase is the database the operator connects to in order to<br />create or drop the Odoo database. | postgres |  |


#### OdooDatabaseStatus



OdooDatabaseStatus records the database the operator resolved for this OdooDeployment.



_Appears in:_
- [OdooDeploymentStatus](#odoodeploymentstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the database |  |  |
| `host` _string_ | Host the database was resolved on |  |  |
| `provisionedBy` _string_ | ProvisionedBy is "operator" when the operator created the database and<br />"external" when it adopted a pre-existing one. Only operator-provisioned<br />databases are ever dropped. |  | Enum: [operator external] <br /> |
| `state` _string_ | State is Provisioning while CREATE DATABASE is in flight and Ready afterwards. |  | Enum: [Provisioning Ready] <br /> |
| `createdAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | CreatedAt is set when the operator created the database. |  |  |


#### OdooDeployment



OdooDeployment is the Schema for the odoodeployments API





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `odoo.abugharbia.com/v1` | | |
| `kind` _string_ | `OdooDeployment` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[OdooDeploymentSpec](#odoodeploymentspec)_ |  |  |  |
| `status` _[OdooDeploymentStatus](#odoodeploymentstatus)_ |  |  |  |


#### OdooDeploymentSpec



OdooDeploymentSpec defines the desired state of OdooDeployment



_Appears in:_
- [OdooDeployment](#odoodeployment)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | The name of the OdooDployment |  |  |
| `odooCommand` _string array_ | The command that runs odoo inside the container, if not specified it will default to "odoo".<br />It replaces the image ENTRYPOINT. | [odoo] | Optional: \{\} <br /> |
| `replicas` _integer_ | The number of replicas to run for the OdooDployment. 0 keeps the<br />database and filestore but runs no pods. | 1 | Minimum: 0 <br /> |
| `image` _string_ | The image to run for the OdooDployment | odoo:18 |  |
| `imagePullPolicy` _[PullPolicy](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#pullpolicy-v1-core)_ | Image pull policy for the OdooDployment | IfNotPresent | Optional: \{\} <br /> |
| `imagePullSecrets` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#localobjectreference-v1-core) array_ | ImagePullSecrets is an optional list of references to secrets in the same namespace to use for pulling any of the images used by this OdooDeployment.<br />If specified, these secrets will be passed to individual puller implementations for them to use. |  | Optional: \{\} <br /> |
| `database` _[OdooDatabaseConfig](#odoodatabaseconfig)_ | The database configuration for the OdooDployment |  |  |
| `config` _[OdooConfig](#odooconfig)_ | The configuration for the Odoo | \{  \} |  |
| `modules` _string array_ | A list of modules to initialise the database with | [base] | MinItems: 1 <br />items:Pattern: `^[A-Za-z0-9_]+$` <br /> |
| `odooFilestore` _[PersistentVolumeClaimSpec](#persistentvolumeclaimspec)_ | PersistentVolumeClaim defines the replicated volume specs | \{  \} | Optional: \{\} <br /> |
| `upgrade` _[OdooUpgradeConfig](#odooupgradeconfig)_ | Upgrade controls module upgrades on image or token changes. | \{  \} | Optional: \{\} <br /> |
| `jobs` _[OdooJobConfig](#odoojobconfig)_ | Jobs tunes the init and upgrade maintenance Jobs. | \{  \} | Optional: \{\} <br /> |
| `probes` _[OdooProbesConfig](#odooprobesconfig)_ | Probes controls the HTTP health probes of the Odoo container. | \{  \} | Optional: \{\} <br /> |
| `env` _[EnvVar](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#envvar-v1-core) array_ | Env is added to the Odoo container (Deployment and Jobs). |  | Optional: \{\} <br /> |
| `envFrom` _[EnvFromSource](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#envfromsource-v1-core) array_ | EnvFrom is added to the Odoo container (Deployment and Jobs). |  | Optional: \{\} <br /> |
| `resources` _[ResourceRequirements](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#resourcerequirements-v1-core)_ | Resources of the Odoo container (Deployment and Jobs). |  | Optional: \{\} <br /> |
| `podSecurityContext` _[PodSecurityContext](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#podsecuritycontext-v1-core)_ | PodSecurityContext of the Odoo pods (Deployment and Jobs). When unset the<br />operator uses runAsUser 100, runAsGroup 101, fsGroup 101, runAsNonRoot. |  | Optional: \{\} <br /> |
| `maintenancePage` _[MaintenancePageConfig](#maintenancepageconfig)_ | MaintenancePage serves a status page instead of an empty Service while<br />Odoo is being installed, upgraded or started. | \{  \} | Optional: \{\} <br /> |
| `backup` _[OdooBackupConfig](#odoobackupconfig)_ | Backup says where this instance's backups (OdooBackup) are written and<br />how they and in-place restores (OdooRestore) run. | \{  \} | Optional: \{\} <br /> |
| `bootstrap` _[Bootstrap](#bootstrap)_ | Bootstrap says how the database is made on first boot. Without it a<br />new database is initialized with spec.modules. |  | Optional: \{\} <br /> |


#### OdooDeploymentStatus



OdooDeploymentStatus defines the observed state of OdooDeployment



_Appears in:_
- [OdooDeployment](#odoodeployment)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the spec generation the status reflects. |  |  |
| `phase` _string_ | Phase summarises the lifecycle: Pending, Initializing, Upgrading, Running, Failed. |  | Enum: [Pending Initializing Upgrading Running Failed BackingUp Restoring] <br /> |
| `appliedImage` _string_ | AppliedImage is the image the Deployment runs (the last image a<br />maintenance Job succeeded with, or spec.image for a plain roll-out). |  |  |
| `appliedUpgradeToken` _string_ | AppliedUpgradeToken is the spec.upgrade.token the last upgrade ran with. |  |  |
| `database` _[OdooDatabaseStatus](#odoodatabasestatus)_ | Database records the resolved database and who provisioned it. |  |  |
| `readyReplicas` _integer_ | ReadyReplicas mirrors the Deployment's available replicas. |  |  |
| `odooConfigSecretName` _string_ | The name of the Secret used to store the Odoo configuration file |  | Optional: \{\} <br /> |
| `odooDataPvcName` _string_ | The name of the PVC used for the Odoo data |  | Optional: \{\} <br /> |
| `initModulesInstalled` _string array_ |  | \{  \} | Optional: \{\} <br /> |
| `currentInitJob` _[MaintenanceJobStatus](#maintenancejobstatus)_ | The maintenance Job (init or upgrade) currently running |  | Optional: \{\} <br /> |
| `currentOperation` _[OperationStatus](#operationstatus)_ | CurrentOperation is the backup, restore or cleanup in progress. |  | Optional: \{\} <br /> |
| `backup` _[OdooBackupScheduleStatus](#odoobackupschedulestatus)_ | Backup reports the backup schedule and the last restore drill. |  | Optional: \{\} <br /> |
| `preUpgradeBackup` _[PreUpgradeBackupStatus](#preupgradebackupstatus)_ | PreUpgradeBackup is the backup spec.backup.beforeUpgrade takes before<br />the pending upgrade. It is cleared once no upgrade is pending. |  | Optional: \{\} <br /> |
| `bootstrap` _[BootstrapStatus](#bootstrapstatus)_ | Bootstrap is the restore spec.bootstrap.fromBackup started. |  | Optional: \{\} <br /> |
| `lastMaintenanceDuration` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#duration-v1-meta)_ | LastMaintenanceDuration is how long the last successful upgrade Job<br />took, from its creation to its completion. The maintenance page uses it<br />to estimate the next one. Init Jobs are not counted: they run once and<br />take far longer than an upgrade. |  | Optional: \{\} <br /> |
| `odooAdminSecretName` _string_ | The secret name for the Odoo admin password |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#condition-v1-meta) array_ |  |  | Optional: \{\} <br /> |


#### OdooDrillResult



OdooDrillResult is the outcome of one restore drill.



_Appears in:_
- [OdooBackupScheduleStatus](#odoobackupschedulestatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `backup` _string_ | Backup is the OdooBackup the drill restored. |  |  |
| `snapshotID` _string_ | SnapshotID is the snapshot the drill restored. |  |  |
| `succeeded` _boolean_ | Succeeded is true when the backup restored, passed every check and<br />Odoo read it. |  |  |
| `message` _string_ | Message explains a failure, or sums up a success. |  |  |
| `startedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | StartedAt and CompletedAt bound the drill. |  |  |
| `completedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ |  |  |  |
| `job` _string_ | Job ran the drill; a failed drill's Job is kept for its log. |  |  |


#### OdooJobConfig



OdooJobConfig tunes the maintenance Jobs (init and upgrade).



_Appears in:_
- [OdooDeploymentSpec](#odoodeploymentspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `activeDeadlineSeconds` _integer_ |  | 3600 | Minimum: 1 <br /> |
| `ttlSecondsAfterFinished` _integer_ | TTLSecondsAfterFinished removes the instance's backup, restore and<br />drill Jobs that long after they finish. Init and upgrade Jobs get no<br />TTL: the operator deletes them when they succeed and keeps a failed one<br />until it is deleted, which is how it is retried. | 86400 | Minimum: 0 <br /> |
| `backoffLimit` _integer_ |  | 2 | Minimum: 0 <br /> |


#### OdooOptionValue

_Underlying type:_ _string_

OdooOptionValue is one odoo.conf value: a single line of at most 4096 characters.

_Validation:_
- MaxLength: 4096
- Pattern: `^[^\n\r]*$`

_Appears in:_
- [OdooConfig](#odooconfig)



#### OdooProbesConfig



OdooProbesConfig controls the HTTP probes on the Odoo container.



_Appears in:_
- [OdooDeploymentSpec](#odoodeploymentspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled turns the default /web/health probes on or off. | true |  |
| `readiness` _[Probe](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#probe-v1-core)_ | Readiness replaces the default readiness probe when set. |  |  |
| `liveness` _[Probe](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#probe-v1-core)_ | Liveness replaces the default liveness probe when set. |  |  |
| `startup` _[Probe](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#probe-v1-core)_ | Startup replaces the default startup probe when set. |  |  |


#### OdooRestore



OdooRestore replaces an OdooDeployment's database and filestore with a
backup, in place. Odoo is stopped while it runs; the replaced data is kept
for keepPrevious so the restore can be undone.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `odoo.abugharbia.com/v1` | | |
| `kind` _string_ | `OdooRestore` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[OdooRestoreSpec](#odoorestorespec)_ |  |  |  |
| `status` _[OdooRestoreStatus](#odoorestorestatus)_ |  |  |  |


#### OdooRestoreConfig



OdooRestoreConfig holds the defaults of in-place restores.



_Appears in:_
- [OdooBackupConfig](#odoobackupconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `keepPrevious` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#duration-v1-meta)_ | KeepPrevious is how long the database and files a restore replaced<br />are kept, so the restore can be undone. | 72h |  |


#### OdooRestoreSpec



OdooRestoreSpec says which backup replaces an instance's data.



_Appears in:_
- [OdooRestore](#odoorestore)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `odooDeployment` _string_ | OdooDeployment whose database and filestore are replaced, in the same namespace. |  | MaxLength: 253 <br />MinLength: 1 <br /> |
| `backup` _string_ | Backup is the OdooBackup to restore. |  | MinLength: 1 <br /> |
| `snapshotID` _string_ | SnapshotID is a restic snapshot in the instance's own repository on<br />its first store (spec.backup.stores[0]), for a backup that has no<br />OdooBackup any more. |  | Pattern: `^[0-9a-f]\{8,64\}$` <br /> |
| `undo` _string_ | Undo reverses the OdooRestore of this name: the database and files it<br />replaced go back in place, and the ones it restored are kept as the<br />previous data of this restore. Refused once that data has been<br />removed (keepPrevious passed) or when another restore ran on the<br />instance since. |  | MinLength: 1 <br /> |
| `from` _[BackupSource](#backupsource)_ | From is a backup in any store: an Odoo .zip or .dump file, or a<br />snapshot in another restic repository, such as another instance's<br />or another cluster's. |  | Optional: \{\} <br /> |
| `neutralize` _boolean_ | Neutralize runs Odoo's neutralize on the restored database before it<br />is swapped in, and gives it a new database.uuid: scheduled actions,<br />outgoing mail servers, payment providers and similar integrations<br />are turned off, so a copy of production cannot act as production.<br />It runs with the instance's image. |  |  |
| `keepPrevious` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#duration-v1-meta)_ | KeepPrevious is how long the replaced database and files are kept<br />for an undo. Overrides spec.backup.restore.keepPrevious. |  | Optional: \{\} <br /> |


#### OdooRestoreStatus



OdooRestoreStatus describes the restore.



_Appears in:_
- [OdooRestore](#odoorestore)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _string_ | Phase: Pending, Stopping (Odoo is being stopped), Restoring,<br />Completed or Failed. |  | Enum: [Pending Stopping Restoring Completed Failed] <br /> |
| `message` _string_ | Message explains the phase in a sentence. |  |  |
| `suffix` _string_ | Suffix names the database and filestore folders of this restore:<br />&lt;db>_restore_&lt;suffix> while restoring, &lt;db>_prev_&lt;suffix> for the<br />data it replaced. |  |  |
| `store` _string_ | Store and SnapshotID are the backup that was restored; File is the<br />Odoo backup file restored from Store instead of a snapshot. |  |  |
| `snapshotID` _string_ |  |  |  |
| `file` _string_ |  |  |  |
| `image` _string_ | Image is the Odoo image recorded in the backup; the instance is<br />started on it. |  |  |
| `odooVersion` _string_ | OdooVersion is the Odoo series in the backup. |  |  |
| `job` _string_ | Job is the Job running the restore. Attempts counts the Jobs that<br />ran it. |  |  |
| `attempts` _integer_ |  |  |  |
| `liveChanged` _boolean_ | LiveChanged is true once the restore may have renamed the live<br />database or files; from then on it is finished by retrying, never<br />abandoned. |  |  |
| `retryAfter` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | RetryAfter is when the next attempt (of the restore or its cleanup)<br />starts after a failed one. |  |  |
| `startedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ |  |  |  |
| `completedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ |  |  |  |
| `previous` _[RestorePrevious](#restoreprevious)_ | Previous is the data this restore replaced. |  |  |
| `cleanedUp` _boolean_ | CleanedUp is true once the cleanup Job deleted what the restore left<br />behind (the replaced data once keepPrevious passed, any half-restored<br />copy, its journal). CleanupAttempts counts its Jobs. |  |  |
| `cleanupAttempts` _integer_ |  |  |  |
| `warnings` _string array_ | Warnings found while checking the restored data. |  |  |


#### OdooUpgradeConfig



OdooUpgradeConfig describes how module upgrades (-u) are triggered.



_Appears in:_
- [OdooDeploymentSpec](#odoodeploymentspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `modules` _string array_ | Modules to upgrade (-u) when an upgrade is triggered. Empty means the new<br />image is rolled out without running a maintenance Job. |  | items:Pattern: `^[A-Za-z0-9_]+$` <br /> |
| `onImageChange` _boolean_ | OnImageChange triggers an upgrade whenever spec.image changes. | true |  |
| `token` _string_ | Token triggers an upgrade whenever its value changes; use it to run<br />upgrades on demand (e.g. for production, together with onImageChange: false). |  |  |


#### OperationStatus



OperationStatus records the backup, restore or cleanup Job an instance is
running. Only one Job (init, upgrade, backup, restore or cleanup) runs per
instance at a time.



_Appears in:_
- [OdooDeploymentStatus](#odoodeploymentstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `kind` _string_ | Kind: backup, restore, cleanup or drill. |  | Enum: [backup restore cleanup drill] <br /> |
| `name` _string_ | Name of the OdooBackup or OdooRestore. |  |  |
| `job` _string_ | Job running it; empty while the operator is still stopping Odoo for it. |  |  |
| `stoppedOdoo` _boolean_ | StoppedOdoo is true when the operation scaled Odoo to zero and must<br />bring it back. |  |  |
| `storeReached` _boolean_ | StoreReached is true once an offline backup has checked, with Odoo<br />still serving, that its store can be reached. |  |  |
| `startedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | StartedAt is when the operation started. |  |  |


#### PVCStore



PVCStore is a PersistentVolumeClaim in the store's namespace, mounted into
the backup Jobs. A ReadWriteOnce claim only works when the Jobs can run on
the node that has it attached.



_Appears in:_
- [OdooBackupStoreSpec](#odoobackupstorespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `claimName` _string_ | ClaimName of the PersistentVolumeClaim. |  | MinLength: 1 <br /> |
| `subPath` _[StorePath](#storepath)_ | SubPath is a folder inside the claim. Repositories go under &lt;subPath>/&lt;namespace>/&lt;deployment>/. |  | MaxLength: 512 <br />Pattern: `^/?([A-Za-z0-9._~+@=-]+(/[A-Za-z0-9._~+@=-]+)*/?)?$` <br /> |


#### PersistentVolumeClaimSpec







_Appears in:_
- [OdooDeploymentSpec](#odoodeploymentspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `size` _[Quantity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#quantity-resource-api)_ | StorageSize defines the size of the new persistent volume claim | 10Gi | Optional: \{\} <br /> |
| `storageClassName` _string_ | StorageClass is the storageClassName used to create a new persistent volume claim | standard | Optional: \{\} <br /> |
| `accessModes` _[PersistentVolumeAccessMode](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#persistentvolumeaccessmode-v1-core) array_ | AccessMode defines the access mode of the new persistent volume claim | [ReadWriteOnce] | Optional: \{\} <br /> |
| `deletionPolicy` _[DeletionPolicy](#deletionpolicy)_ | DeletionPolicy controls whether the filestore PVC is garbage collected<br />with the OdooDeployment (Delete) or left behind (Retain, the default). | Retain | Enum: [Retain Delete] <br /> |


#### PreUpgradeBackupStatus



PreUpgradeBackupStatus names the backup taken before an upgrade.



_Appears in:_
- [OdooDeploymentStatus](#odoodeploymentstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the OdooBackup. |  | Optional: \{\} <br /> |
| `target` _string_ | Target is the image and upgrade token it was taken for. |  | Optional: \{\} <br /> |


#### RestorePrevious



RestorePrevious is the data an OdooRestore replaced.



_Appears in:_
- [OdooRestoreStatus](#odoorestorestatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `state` _string_ | State: Kept, Removed, Undone or None. |  |  |
| `database` _string_ | Database is the replaced database's name while it is kept. |  |  |
| `filestore` _string_ | Filestore is the replaced filestore folder, under &lt;dataDir>/filestore/. |  |  |
| `keptUntil` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | KeptUntil is when the replaced data is deleted. |  |  |
| `undoneBy` _string_ | UndoneBy is the OdooRestore that put the data back. |  |  |
| `image` _string_ | Image and UpgradeToken are what the instance ran on the replaced<br />data; an undo starts Odoo on them again. |  |  |
| `upgradeToken` _string_ |  |  |  |




#### S3Store



S3Store is an S3 bucket, on AWS or any S3-compatible service.



_Appears in:_
- [OdooBackupStoreSpec](#odoobackupstorespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `endpoint` _string_ | Endpoint of an S3-compatible service, such as https://s3.example.com.<br />Leave it out for AWS. |  | Pattern: `^https?://[^/\s]+/?$` <br /> |
| `region` _string_ | Region of the bucket. Optional for most S3-compatible services. |  |  |
| `bucket` _string_ | Bucket holding the backups. |  | MaxLength: 63 <br />MinLength: 3 <br />Pattern: `^[a-z0-9][a-z0-9.-]*[a-z0-9]$` <br /> |
| `path` _[StorePath](#storepath)_ | Path inside the bucket. Repositories go under &lt;path>/&lt;namespace>/&lt;deployment>/. |  | MaxLength: 512 <br />Pattern: `^/?([A-Za-z0-9._~+@=-]+(/[A-Za-z0-9._~+@=-]+)*/?)?$` <br /> |
| `forcePathStyle` _boolean_ | ForcePathStyle addresses the bucket as &lt;endpoint>/&lt;bucket> instead of<br />&lt;bucket>.&lt;endpoint>. Most S3-compatible services need it; AWS does not. |  |  |
| `credentialsSecret` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#localobjectreference-v1-core)_ | CredentialsSecret holds AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY. |  |  |


#### SFTPStore



SFTPStore is a folder on an SSH server.



_Appears in:_
- [OdooBackupStoreSpec](#odoobackupstorespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `host` _string_ | Host of the SSH server. |  | MinLength: 1 <br />Pattern: `^[A-Za-z0-9.:\[\]-]+$` <br /> |
| `port` _integer_ | Port of the SSH server. | 22 | Maximum: 65535 <br />Minimum: 1 <br /> |
| `user` _string_ | User to log in as. |  | MinLength: 1 <br />Pattern: `^[A-Za-z0-9._-]+$` <br /> |
| `path` _[StorePath](#storepath)_ | Path on the server, relative to the user's home folder unless it<br />starts with "/". Repositories go under &lt;path>/&lt;namespace>/&lt;deployment>/. |  | MaxLength: 512 <br />Pattern: `^/?([A-Za-z0-9._~+@=-]+(/[A-Za-z0-9._~+@=-]+)*/?)?$` <br /> |
| `sshSecret` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#localobjectreference-v1-core)_ | SSHSecret holds the private key and the server's host key<br />(known_hosts). The private key is under id_ed25519 or, as in Secrets of<br />type kubernetes.io/ssh-auth, ssh-privatekey (id_ed25519 wins when both<br />are set). Any unencrypted Ed25519, ECDSA or RSA key works, in OpenSSH or<br />PEM format; keys with a passphrase do not. The host key is always<br />checked. |  |  |


#### StoreCheck



StoreCheck checks the store's repositories on a schedule.



_Appears in:_
- [OdooBackupStoreSpec](#odoobackupstorespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `schedule` _string_ | Schedule in cron format ("0 5 * * 0" or @weekly); empty turns checks off. |  | MaxLength: 100 <br /> |
| `readDataParts` _integer_ | ReadDataParts: each check reads one of this many parts of the stored<br />data, the next part on the next run, so all data is read every<br />ReadDataParts checks. 1 reads everything every time; 0 reads only the<br />repository structure. | 7 | Maximum: 100 <br />Minimum: 0 <br /> |


#### StorePath

_Underlying type:_ _string_

StorePath is a folder inside a store: relative segments separated by "/",
never "..". A leading "/" makes an SFTP path absolute; elsewhere it is
ignored.

_Validation:_
- MaxLength: 512
- Pattern: `^/?([A-Za-z0-9._~+@=-]+(/[A-Za-z0-9._~+@=-]+)*/?)?$`

_Appears in:_
- [PVCStore](#pvcstore)
- [S3Store](#s3store)
- [SFTPStore](#sftpstore)



#### StoreSchedule



StoreSchedule runs a store-wide task on a schedule.



_Appears in:_
- [OdooBackupStoreSpec](#odoobackupstorespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `schedule` _string_ | Schedule in cron format ("0 4 * * 0" or @weekly); empty turns it off. |  | MaxLength: 100 <br /> |


#### StoreTaskStatus



StoreTaskStatus is the outcome of the latest check or prune of a store.



_Appears in:_
- [OdooBackupStoreStatus](#odoobackupstorestatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `time` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.34/#time-v1-meta)_ | Time the task finished. |  |  |
| `result` _string_ | Result: Passed or Failed. |  | Enum: [Passed Failed] <br /> |
| `message` _string_ | Message sums up the outcome, or names what failed. |  |  |
| `job` _string_ | Job that ran it; a failed task's Job is kept for its log. |  |  |
| `repositories` _string array_ | Repositories the task went through. |  |  |
| `part` _string_ | Part is the part of the data a check read, such as 3/7. |  |  |


