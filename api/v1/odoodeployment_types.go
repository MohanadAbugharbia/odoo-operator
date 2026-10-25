/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

const (
	// DatabaseFinalizer is set on OdooDeployments whose database the operator
	// created and is expected to drop (spec.database.deletionPolicy: Delete).
	DatabaseFinalizer = "odoo.abugharbia.com/database"
	// OperationsFinalizer holds an OdooDeployment without the database
	// finalizer (deletionPolicy Retain, or an adopted database) while one of
	// its Jobs runs a backup, restore, cleanup, drill, init or upgrade, so
	// that deleting the instance waits for the Job's pods to finish instead
	// of killing one half way through. It is removed once no such Job runs.
	OperationsFinalizer = "odoo.abugharbia.com/operations"

	// LabelOdooDeployment marks every pod (Deployment and Job) that belongs to an OdooDeployment.
	LabelOdooDeployment = "odoo.abugharbia.com/odoodeployment"
	// LabelJobKind marks maintenance Job pods with "init" or "upgrade", and
	// image check Job pods with "image-check".
	LabelJobKind = "odoo.abugharbia.com/job-kind"
	// AnnotationConfigHash carries the sha256 of the rendered odoo.conf on the
	// Deployment pod template so that config changes roll the pods.
	AnnotationConfigHash = "odoo.abugharbia.com/config-hash"
	// AnnotationMaintenanceStatus carries the hash of the maintenance page's
	// status on its pods; changing it makes the kubelet refresh the mounted
	// status at once instead of on its next periodic sync.
	AnnotationMaintenanceStatus = "odoo.abugharbia.com/maintenance-status"

	JobKindInit    = "init"
	JobKindUpgrade = "upgrade"
	// JobKindImageCheck is the short Job that proves a new spec.image can be
	// pulled before the running Deployment is scaled down for it.
	JobKindImageCheck = "image-check"

	// LabelMaintenancePage selects the maintenance page pods of an
	// OdooDeployment. They never carry the Odoo selector or
	// LabelOdooDeployment, so they are not counted as Odoo pods.
	LabelMaintenancePage = "odoo.abugharbia.com/maintenance-page"
)

// Condition types.
const (
	ConditionReady         = "Ready"
	ConditionDatabaseReady = "DatabaseReady"
	ConditionInitialized   = "Initialized"
	// ConditionDegraded is True only when something is wrong.
	ConditionDegraded = "Degraded"
	// ConditionBackupSchedule is True while spec.backup.schedule or the
	// drill schedule is set and valid, False when one cannot be read.
	ConditionBackupSchedule = "BackupSchedule"
)

// Phases.
const (
	PhasePending      = "Pending"
	PhaseInitializing = "Initializing"
	PhaseUpgrading    = "Upgrading"
	PhaseRunning      = "Running"
	PhaseFailed       = "Failed"
	// PhaseBackingUp: Odoo is stopped for an Offline backup.
	PhaseBackingUp = "BackingUp"
	// PhaseRestoring: Odoo is stopped while an OdooRestore replaces its data.
	PhaseRestoring = "Restoring"
)

// Database provenance and states.
const (
	DatabaseProvisionedByOperator = "operator"
	DatabaseProvisionedByExternal = "external"

	DatabaseStateProvisioning = "Provisioning"
	DatabaseStateReady        = "Ready"
)

// Condition reasons.
const (
	ReasonDbConnectionDetailsFailed = "DbConnectionDetailsFailed"

	ReasonOdooConfigSecretNotAvailable      = "OdooConfigSecretNotAvailable"
	ReasonOdooConfigSecretCreationFailed    = "OdooConfigSecretCreationFailed"
	ReasonOdooConfigSecretUpdateFailed      = "OdooConfigSecretUpdateFailed"
	ReasonOdooConfigSecretCreationSucceeded = "OdooConfigSecretCreationSucceeded"

	ReasonPvcNotAvailable      = "PvcNotAvailable"
	ReasonPvcCreationFailed    = "PvcCreationFailed"
	ReasonPvcUpdateFailed      = "PvcUpdateFailed"
	ReasonPvcCreationSucceeded = "PvcCreationSucceeded"

	ReasonOdooAdminSecretCreationFailed    = "OdooAdminSecretCreationFailed"
	ReasonOdooAdminSecretNotAvailable      = "OdooAdminSecretNotAvailable"
	ReasonOdooAdminSecretUpdateFailed      = "OdooAdminSecretUpdateFailed"
	ReasonOdooAdminSecretCreationSucceeded = "OdooAdminSecretCreationSucceeded"
	ReasonOdooAdminPasswordFailed          = "OdooAdminPasswordFailed"

	ReasonFailedGetHttpService    = "FailedGetHttpService"
	ReasonFailedCreateHttpService = "FailedCreateHttpService"
	ReasonFailedUpdateHttpService = "FailedUpdateHttpService"

	ReasonFailedGetPollService    = "FailedGetPollService"
	ReasonFailedCreatePollService = "FailedCreatePollService"
	ReasonFailedUpdatePollService = "FailedUpdatePollService"

	ReasonDatabaseConnectionFailed = "DatabaseConnectionFailed"
	ReasonDatabaseCreated          = "DatabaseCreated"
	ReasonDatabaseAdopted          = "DatabaseAdopted"
	ReasonDatabaseMissing          = "DatabaseMissing"
	ReasonDatabaseProvisioning     = "DatabaseProvisioning"
	ReasonDatabaseCreateFailed     = "DatabaseCreateFailed"
	ReasonDatabaseDropFailed       = "DatabaseDropFailed"
	ReasonDatabaseDropped          = "DatabaseDropped"
	ReasonDatabaseDropRefused      = "DatabaseDropRefused"
	ReasonDatabaseNotDropped       = "DatabaseNotDropped"
	ReasonDatabaseNameChanged      = "DatabaseNameChanged"
	ReasonDatabaseReady            = "DatabaseReady"

	ReasonInitJobCreated        = "InitJobCreated"
	ReasonInitJobRunning        = "InitJobRunning"
	ReasonInitJobSucceeded      = "InitJobSucceeded"
	ReasonInitJobFailed         = "InitJobFailed"
	ReasonUpgradeJobCreated     = "UpgradeJobCreated"
	ReasonUpgradeJobRunning     = "UpgradeJobRunning"
	ReasonUpgradeJobSucceeded   = "UpgradeJobSucceeded"
	ReasonUpgradeJobFailed      = "UpgradeJobFailed"
	ReasonJobCreationFailed     = "JobCreationFailed"
	ReasonWaitingForPodsToStop  = "WaitingForPodsToStop"
	ReasonQuotaExceeded         = "QuotaExceeded"
	ReasonScaledToZero          = "ScaledToZero"
	ReasonDeploymentAvailable   = "DeploymentAvailable"
	ReasonDeploymentProgressing = "DeploymentProgressing"
	ReasonDeploymentFailed      = "DeploymentFailed"
	ReasonNotInitialized        = "NotInitialized"
	ReasonReconcileSucceeded    = "ReconcileSucceeded"
	ReasonReconcileFailed       = "ReconcileFailed"
	ReasonInvalidSpec           = "InvalidSpec"
	ReasonDeleting              = "Deleting"

	ReasonMaintenancePageFailed = "MaintenancePageFailed"
	// ReasonMaintenancePageImageUnknown is the warning event while the
	// operator cannot tell its own image, so it serves no maintenance page.
	ReasonMaintenancePageImageUnknown = "MaintenancePageImageUnknown"
)

// Image pull reasons.
const (
	// ReasonImagePullFailed: a Job's pod cannot pull spec.image (ErrImagePull,
	// ImagePullBackOff, …). Before an upgrade this keeps the old image serving.
	ReasonImagePullFailed = "ImagePullFailed"
	// ReasonImageCheckFailed: the image pulled but `<odooCommand> --version` failed.
	ReasonImageCheckFailed = "ImageCheckFailed"
	// ReasonPreUpgradeBackupFailed: beforeUpgrade is Required and the backup
	// before the upgrade failed, so the running image keeps serving.
	ReasonPreUpgradeBackupFailed = "PreUpgradeBackupFailed"
	// ReasonBootstrapFailed: the OdooRestore of spec.bootstrap.fromBackup
	// failed; Odoo does not start until it is deleted (which tries again).
	ReasonBootstrapFailed = "BootstrapFailed"
	// ReasonStoreReadOnly: a store in spec.backup.stores is readOnly.
	ReasonStoreReadOnly = "StoreReadOnly"
	// ReasonJobSuperseded: a Job stuck pulling an image was replaced because
	// spec.image moved on.
	ReasonJobSuperseded = "JobSuperseded"
)

type DatabaseConnectionDetails struct {
	Host     string
	Port     int32
	User     string
	Password string
	Name     string
	SSL      bool
	MaxConn  int32
}

// DatabaseCreatePolicy tells the operator what to do when the database named in
// spec.database does not exist.
// +kubebuilder:validation:Enum=IfNotExists;Never
type DatabaseCreatePolicy string

const (
	// DatabaseCreatePolicyIfNotExists creates the database when it is missing and
	// adopts it as an external database when it already exists.
	DatabaseCreatePolicyIfNotExists DatabaseCreatePolicy = "IfNotExists"
	// DatabaseCreatePolicyNever never creates a database; a missing database is reported as Degraded.
	DatabaseCreatePolicyNever DatabaseCreatePolicy = "Never"
)

// DeletionPolicy tells the operator what to do with an owned resource when the
// OdooDeployment is deleted.
// +kubebuilder:validation:Enum=Retain;Delete
type DeletionPolicy string

const (
	DeletionPolicyRetain DeletionPolicy = "Retain"
	DeletionPolicyDelete DeletionPolicy = "Delete"
)

// OdooDatabaseConfig defines the database connection configuration for Odoo
type OdooDatabaseConfig struct {
	// The database host to use for Odoo
	// +kubebuilder:default="postgresql"
	Host string `json:"host,omitempty"`
	// The database host to use for Odoo from a secret
	HostFromSecret corev1.SecretKeySelector `json:"hostFromSecret,omitempty"`

	// The database port to use for Odoo
	// +kubebuilder:default=5432
	Port int32 `json:"port,omitempty"`
	// The database port to use for Odoo from a secret
	PortFromSecret corev1.SecretKeySelector `json:"portFromSecret,omitempty"`

	// The database user to use for Odoo
	// +kubebuilder:default="odoo"
	User string `json:"user,omitempty"`
	// The database user to use for Odoo from a secret
	UserFromSecret corev1.SecretKeySelector `json:"userFromSecret,omitempty"`

	// The database password to use for Odoo
	PasswordFromSecret corev1.SecretKeySelector `json:"passwordFromSecret"`

	// The database name to use for Odoo. Immutable: the operator only ever
	// drops the database it recorded in status.database, so renaming would
	// silently orphan it.
	// +kubebuilder:default="odoo"
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_-]*$`
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.database.name is immutable"
	Name string `json:"name,omitempty"`
	// The database name to use for Odoo from a secret. Immutable for the same reason as name.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.database.nameFromSecret is immutable"
	NameFromSecret corev1.SecretKeySelector `json:"nameFromSecret,omitempty"`

	// Whether or not to enable SSL for the database connection.
	// Rendered as db_sslmode = require (true) or disable (false).
	// +kubebuilder:default=false
	SSL bool `json:"ssl,omitempty"`
	// Whether or not to enable SSL for the database connection from a secret
	SSLFromSecret corev1.SecretKeySelector `json:"sslFromSecret,omitempty"`

	// The database max connections to use for Odoo
	// +kubebuilder:default=20
	MaxConn int32 `json:"maxConn,omitempty"`
	// The database max connections to use for Odoo from a secret
	MaxConnFromSecret corev1.SecretKeySelector `json:"maxConnFromSecret,omitempty"`

	// CreatePolicy controls whether the operator creates the database when it
	// does not exist. A database that already exists is adopted as "external"
	// and is never dropped by the operator.
	// +kubebuilder:default=IfNotExists
	CreatePolicy DatabaseCreatePolicy `json:"createPolicy,omitempty"`

	// DeletionPolicy controls whether a database the operator created is
	// dropped when the OdooDeployment is deleted. Databases the operator did
	// not create (status.database.provisionedBy: external) are always retained.
	// +kubebuilder:default=Retain
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// MaintenanceDatabase is the database the operator connects to in order to
	// create or drop the Odoo database.
	// +kubebuilder:default="postgres"
	MaintenanceDatabase string `json:"maintenanceDatabase,omitempty"`
}

type OdooConfig struct {
	// The admin password to use for the Odoo application
	// The admin passowrd is used to create/copy or delete odoo databases
	// This can be left empty to generate a secure random password
	AdminPasswordSecretName string `json:"adminPasswordSecretName,omitempty"`

	// Enable debug mode for Odoo
	// +kubebuilder:default=false
	DebugMode bool `json:"debugMode,omitempty"`

	// The directory to use for the odoo filestore and session store
	// +kubebuilder:default="/var/lib/odoo"
	DataDir string `json:"dataDir,omitempty"`

	// Install modules without demo data
	// +kubebuilder:default=true
	WithoutDemo *bool `json:"withoutDemo,omitempty"`

	// Proxy Mode for Odoo
	// This instructs Odoo to use the X-Forwarded-For header for the remote IP address
	// +kubebuilder:default=true
	ProxyMode *bool `json:"proxyMode,omitempty"`

	// Numer of process workers to use for Odoo. 0 runs the threaded server.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	Workers *int32 `json:"workers,omitempty"`

	// The maximum number of requests that the process can take
	// +kubebuilder:default=8192
	LimitRequest int32 `json:"limitRequest,omitempty"`

	// The maximum real time in seconds that the process can take
	// +kubebuilder:default=120
	LimitTimeReal int32 `json:"limitTimeReal,omitempty"`

	// The maximum CPU time in seconds that the process can take
	// +kubebuilder:default=60
	LimitTimeCPU int32 `json:"limitTimeCpu,omitempty"`

	// The maximum memory in bytes that the process can take
	// +kubebuilder:default=2147483648
	LimitMemorySoft int64 `json:"limitMemorySoft,omitempty"`

	// The maximum memory in bytes that the process can take
	// +kubebuilder:default=2684354560
	LimitMemoryHard int64 `json:"limitMemoryHard,omitempty"`

	// The maximum number of cron threads to use for Odoo
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	MaxCronThreads *int32 `json:"maxCronThreads,omitempty"`

	// Extra addons paths for Odoo. Each entry must be an absolute path with no commas, spaces, newlines, or # characters.
	// +kubebuilder:validation:Optional
	// +listType=set
	// +kubebuilder:validation:items:Pattern=`^/[^,\n\r# ]+$`
	ExtraAddonsPaths []string `json:"extraAddonsPaths,omitempty"`

	// ListDb controls the database manager. When false (the default) the
	// rendered odoo.conf carries list_db = False and dbfilter = ^<db_name>$ so
	// the instance can only ever see its own database.
	// +kubebuilder:default=false
	ListDb *bool `json:"listDb,omitempty"`

	// DbFilter is written verbatim as dbfilter and replaces the derived
	// ^<db_name>$ filter. Single line.
	// +kubebuilder:validation:Pattern=`^[^\n\r]*$`
	DbFilter string `json:"dbFilter,omitempty"`

	// ServerWideModules is rendered as server_wide_modules (Odoo's default is base,web).
	// +listType=atomic
	// +kubebuilder:validation:items:Pattern=`^[A-Za-z0-9_]+$`
	ServerWideModules []string `json:"serverWideModules,omitempty"`

	// LoadLanguages is passed as --load-language to maintenance Jobs that
	// install modules (-i), so translations are loaded at first boot.
	// +listType=atomic
	// +kubebuilder:validation:items:Pattern=`^[a-z]{2,3}(_[A-Za-z0-9]{2,4})?$`
	LoadLanguages []string `json:"loadLanguages,omitempty"`

	// ExtraOptions are appended verbatim to the [options] section, sorted by
	// key. Keys must be odoo.conf option names (^[a-z_][a-z0-9_]*$, checked by
	// the operator, which reports Degraded/InvalidSpec otherwise); values must
	// be single line (enforced by the schema).
	// +kubebuilder:validation:MaxProperties=64
	ExtraOptions map[string]OdooOptionValue `json:"extraOptions,omitempty"`
}

// OdooOptionValue is one odoo.conf value: a single line of at most 4096 characters.
// +kubebuilder:validation:MaxLength=4096
// +kubebuilder:validation:Pattern=`^[^\n\r]*$`
type OdooOptionValue string

type PersistentVolumeClaimSpec struct {
	// StorageSize defines the size of the new persistent volume claim
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="10Gi"
	Size resource.Quantity `json:"size,omitempty"`

	// StorageClass is the storageClassName used to create a new persistent volume claim
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=standard
	StorageClassName string `json:"storageClassName,omitempty"`
	// AccessMode defines the access mode of the new persistent volume claim
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={"ReadWriteOnce"}
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`

	// DeletionPolicy controls whether the filestore PVC is garbage collected
	// with the OdooDeployment (Delete) or left behind (Retain, the default).
	// +kubebuilder:default=Retain
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// OdooUpgradeConfig describes how module upgrades (-u) are triggered.
type OdooUpgradeConfig struct {
	// Modules to upgrade (-u) when an upgrade is triggered. Empty means the new
	// image is rolled out without running a maintenance Job.
	// +listType=atomic
	// +kubebuilder:validation:items:Pattern=`^[A-Za-z0-9_]+$`
	Modules []string `json:"modules,omitempty"`

	// OnImageChange triggers an upgrade whenever spec.image changes.
	// +kubebuilder:default=true
	OnImageChange *bool `json:"onImageChange,omitempty"`

	// Token triggers an upgrade whenever its value changes; use it to run
	// upgrades on demand (e.g. for production, together with onImageChange: false).
	Token string `json:"token,omitempty"`
}

// OdooJobConfig tunes the maintenance Jobs (init and upgrade).
type OdooJobConfig struct {
	// +kubebuilder:default=3600
	// +kubebuilder:validation:Minimum=1
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`
	// TTLSecondsAfterFinished removes the instance's backup, restore and
	// drill Jobs that long after they finish. Init and upgrade Jobs get no
	// TTL: the operator deletes them when they succeed and keeps a failed one
	// until it is deleted, which is how it is retried.
	// +kubebuilder:default=86400
	// +kubebuilder:validation:Minimum=0
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`
}

// OdooProbesConfig controls the HTTP probes on the Odoo container.
type OdooProbesConfig struct {
	// Enabled turns the default /web/health probes on or off.
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`
	// Readiness replaces the default readiness probe when set.
	Readiness *corev1.Probe `json:"readiness,omitempty"`
	// Liveness replaces the default liveness probe when set.
	Liveness *corev1.Probe `json:"liveness,omitempty"`
	// Startup replaces the default startup probe when set.
	Startup *corev1.Probe `json:"startup,omitempty"`
}

// MaintenancePageText is one line of text shown on the maintenance page.
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=64
// +kubebuilder:validation:Pattern=`^[^\n\r<>]*$`
type MaintenancePageText string

// MaintenancePageConfig controls the page the http Service serves while Odoo
// itself cannot answer: during the init and upgrade Jobs and until the
// Deployment has an available replica again.
type MaintenancePageConfig struct {
	// Enabled runs a small <name>-maintenance Deployment from the operator's
	// own image and points the http Service at it whenever Odoo has no
	// available replica (and spec.replicas is not 0). The poll Service always
	// stays on Odoo.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// Title is the product name the page shows ("Updating <title>").
	// +kubebuilder:default="Odoo"
	Title MaintenancePageText `json:"title,omitempty"`

	// TitleTranslations replaces the title for a page language. The page is
	// shown in English or Arabic, picked from the browser's Accept-Language,
	// so the only key it reads today is "ar".
	// +kubebuilder:validation:MaxProperties=8
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.matches('^[a-z]{2,3}$'))",message="keys must be language codes such as ar"
	TitleTranslations map[string]MaintenancePageText `json:"titleTranslations,omitempty"`

	// AccentColor is the page's accent, as #rrggbb.
	// +kubebuilder:default="#714b67"
	// +kubebuilder:validation:Pattern=`^#[0-9A-Fa-f]{6}$`
	AccentColor string `json:"accentColor,omitempty"`

	// Logo is shown instead of the title's initial. It must be a base64 data
	// URI: Odoo is not answering while the page is up, so the page cannot
	// load images from it.
	// +kubebuilder:validation:MaxLength=65536
	// +kubebuilder:validation:Pattern=`^data:image/(png|jpeg|gif|webp|svg\+xml);base64,[A-Za-z0-9+/]+=*$`
	Logo string `json:"logo,omitempty"`
}

// BeforeUpgrade says whether a backup is taken before an upgrade.
// +kubebuilder:validation:Enum=Off;BestEffort;Required
type BeforeUpgrade string

const (
	// BeforeUpgradeOff upgrades without a backup first.
	BeforeUpgradeOff BeforeUpgrade = "Off"
	// BeforeUpgradeBestEffort takes a backup first and upgrades even when
	// it fails.
	BeforeUpgradeBestEffort BeforeUpgrade = "BestEffort"
	// BeforeUpgradeRequired upgrades only after the backup succeeded; a
	// failed backup keeps the running image.
	BeforeUpgradeRequired BeforeUpgrade = "Required"
)

// BackupConsistency is how a backup keeps the database and the filestore
// in step.
// +kubebuilder:validation:Enum=Online;Offline
type BackupConsistency string

const (
	// BackupConsistencyOnline backs up while Odoo keeps serving: the dump and
	// the list of files come from one database snapshot, and files deleted
	// meanwhile are taken from the previous backup.
	BackupConsistencyOnline BackupConsistency = "Online"
	// BackupConsistencyOffline stops Odoo for the backup.
	BackupConsistencyOffline BackupConsistency = "Offline"
)

// OdooRestoreConfig holds the defaults of in-place restores.
type OdooRestoreConfig struct {
	// KeepPrevious is how long the database and files a restore replaced
	// are kept, so the restore can be undone.
	// +kubebuilder:default="72h"
	KeepPrevious *metav1.Duration `json:"keepPrevious,omitempty"`
}

// OdooBackupRetention says which backups are kept. Scheduled backups are
// thinned with restic's keep rules (daily: 7 keeps the newest backup of each
// of the last 7 days that have one; a backup kept by any rule stays); manual
// backups keep their newest Manual and pre-upgrade backups their newest
// PreUpgrade. A backup with
// spec.keep, and the newest backup, are never deleted. Unset fields take the
// defaults below.
type OdooBackupRetention struct {
	// Last keeps the newest N backups.
	// +kubebuilder:validation:Minimum=0
	Last *int32 `json:"last,omitempty"`
	// Hourly keeps the newest backup of each of the last N hours that have one.
	// +kubebuilder:validation:Minimum=0
	Hourly *int32 `json:"hourly,omitempty"`
	// Daily keeps the newest backup of each of the last N days that have one.
	// +kubebuilder:default=7
	// +kubebuilder:validation:Minimum=0
	Daily *int32 `json:"daily,omitempty"`
	// Weekly keeps the newest backup of each of the last N weeks that have one.
	// +kubebuilder:default=4
	// +kubebuilder:validation:Minimum=0
	Weekly *int32 `json:"weekly,omitempty"`
	// Monthly keeps the newest backup of each of the last N months that have one.
	// +kubebuilder:default=12
	// +kubebuilder:validation:Minimum=0
	Monthly *int32 `json:"monthly,omitempty"`
	// Yearly keeps the newest backup of each of the last N years that have one.
	// +kubebuilder:validation:Minimum=0
	Yearly *int32 `json:"yearly,omitempty"`
	// Within keeps every backup newer than this, counted from the newest
	// backup, such as 30d, 12h or 1y6m (restic's duration format).
	// +kubebuilder:validation:Pattern=`^([0-9]+[ymdh])*$`
	Within string `json:"within,omitempty"`
	// Manual keeps the newest N manual backups; 0 keeps them all.
	// +kubebuilder:validation:Minimum=0
	Manual *int32 `json:"manual,omitempty"`
	// PreUpgrade keeps the newest N backups taken before upgrades; 0 keeps
	// them all.
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=0
	PreUpgrade *int32 `json:"preUpgrade,omitempty"`
	// KeepAll turns retention off: no backup is ever deleted.
	KeepAll bool `json:"keepAll,omitempty"`
}

// OdooBackupDrill restores the newest backup on a schedule to prove it can
// be restored, without touching the running instance.
type OdooBackupDrill struct {
	// Schedule in cron format, in spec.backup.timeZone; empty turns drills off.
	// +kubebuilder:validation:MaxLength=100
	Schedule string `json:"schedule,omitempty"`
	// ScratchSizeLimit caps the node disk the drill's scratch filestore
	// (an emptyDir) may use; unset leaves it uncapped. A drill that needs
	// more is evicted and reported failed.
	// +optional
	ScratchSizeLimit *resource.Quantity `json:"scratchSizeLimit,omitempty"`
}

// OdooBackupConfig turns on backups of this instance.
//
// +kubebuilder:validation:XValidation:rule="!has(self.schedule) || size(self.schedule) == 0 || (has(self.stores) && size(self.stores) > 0)",message="spec.backup.schedule needs spec.backup.stores"
// +kubebuilder:validation:XValidation:rule="!has(self.drill) || !has(self.drill.schedule) || size(self.drill.schedule) == 0 || (has(self.stores) && size(self.stores) > 0)",message="spec.backup.drill.schedule needs spec.backup.stores"
// +kubebuilder:validation:XValidation:rule="!has(self.beforeUpgrade) || self.beforeUpgrade == 'Off' || (has(self.stores) && size(self.stores) > 0)",message="spec.backup.beforeUpgrade needs spec.backup.stores"
type OdooBackupConfig struct {
	// Stores are OdooBackupStores in this namespace. Backups are written to
	// the first; the instance's repository is <path>/<namespace>/<name>/
	// inside it. Each backup is then copied to the other stores.
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self.all(x, self.exists_one(y, y == x))",message="spec.backup.stores must not repeat a store"
	// +listType=atomic
	Stores []string `json:"stores,omitempty"`

	// Schedule takes a backup on a cron schedule ("0 3 * * *" or @daily), in
	// TimeZone. Empty (the default) takes no scheduled backups. A schedule
	// missed while the operator was down runs once when it is back.
	// +kubebuilder:validation:MaxLength=100
	Schedule string `json:"schedule,omitempty"`

	// TimeZone of Schedule, the drill schedule and the retention rules, as an
	// IANA name such as Europe/Berlin.
	// +kubebuilder:default=UTC
	// +kubebuilder:validation:MaxLength=100
	TimeZone string `json:"timeZone,omitempty"`

	// Retention says which backups are kept after each backup.
	// +kubebuilder:default={}
	Retention OdooBackupRetention `json:"retention,omitempty"`

	// Drill restores the newest backup on a schedule to prove it works.
	// +kubebuilder:validation:Optional
	Drill OdooBackupDrill `json:"drill,omitempty"`

	// BeforeUpgrade takes a backup before each upgrade (a new spec.image or
	// spec.upgrade.token), with Odoo stopped so the backup holds the data
	// exactly as the upgrade found it. Off (default) upgrades without one;
	// BestEffort upgrades even when the backup fails; Required keeps the
	// running image when it fails, until the failed OdooBackup is deleted
	// (which tries again) or the upgrade target changes.
	// +kubebuilder:default=Off
	BeforeUpgrade BeforeUpgrade `json:"beforeUpgrade,omitempty"`

	// Consistency: Online (default) backs up while Odoo keeps serving;
	// Offline stops Odoo for the backup.
	// +kubebuilder:default=Online
	Consistency BackupConsistency `json:"consistency,omitempty"`

	// Restore holds the defaults of in-place restores (OdooRestore).
	// +kubebuilder:default={}
	Restore OdooRestoreConfig `json:"restore,omitempty"`

	// Resources of the backup and restore Jobs.
	// +kubebuilder:validation:Optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// ActiveDeadlineSeconds bounds one backup or restore Job.
	// +kubebuilder:default=21600
	// +kubebuilder:validation:Minimum=60
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`
}

// OdooDeploymentSpec defines the desired state of OdooDeployment
type OdooDeploymentSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// The name of the OdooDployment
	Name string `json:"name"`

	// The command that runs odoo inside the container, if not specified it will default to "odoo".
	// It replaces the image ENTRYPOINT.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={"odoo"}
	OdooCommand []string `json:"odooCommand,omitempty"`

	// The number of replicas to run for the OdooDployment. 0 keeps the
	// database and filestore but runs no pods.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`
	// The image to run for the OdooDployment
	// +kubebuilder:default="odoo:18"
	Image string `json:"image,omitempty"`

	// Image pull policy for the OdooDployment
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="IfNotPresent"
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// ImagePullSecrets is an optional list of references to secrets in the same namespace to use for pulling any of the images used by this OdooDeployment.
	// If specified, these secrets will be passed to individual puller implementations for them to use.
	// +kubebuilder:validation:Optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// The database configuration for the OdooDployment
	Database OdooDatabaseConfig `json:"database"`
	// The configuration for the Odoo
	// +kubebuilder:default={}
	Config OdooConfig `json:"config,omitempty"`

	// A list of modules to initialise the database with
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:default={"base"}
	// +kubebuilder:validation:items:Pattern=`^[A-Za-z0-9_]+$`
	Modules []string `json:"modules,omitempty"`

	// PersistentVolumeClaim defines the replicated volume specs
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	OdooFilestore PersistentVolumeClaimSpec `json:"odooFilestore,omitempty"`

	// Upgrade controls module upgrades on image or token changes.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	Upgrade OdooUpgradeConfig `json:"upgrade,omitempty"`

	// Jobs tunes the init and upgrade maintenance Jobs.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	Jobs OdooJobConfig `json:"jobs,omitempty"`

	// Probes controls the HTTP health probes of the Odoo container.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	Probes OdooProbesConfig `json:"probes,omitempty"`

	// Env is added to the Odoo container (Deployment and Jobs).
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Env []corev1.EnvVar `json:"env,omitempty"`

	// EnvFrom is added to the Odoo container (Deployment and Jobs).
	// +kubebuilder:validation:Optional
	// +listType=atomic
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`

	// Resources of the Odoo container (Deployment and Jobs).
	// +kubebuilder:validation:Optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// PodSecurityContext of the Odoo pods (Deployment and Jobs). When unset the
	// operator uses runAsUser 100, runAsGroup 101, fsGroup 101, runAsNonRoot.
	// +kubebuilder:validation:Optional
	PodSecurityContext *corev1.PodSecurityContext `json:"podSecurityContext,omitempty"`

	// MaintenancePage serves a status page instead of an empty Service while
	// Odoo is being installed, upgraded or started.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	MaintenancePage MaintenancePageConfig `json:"maintenancePage,omitempty"`

	// Backup says where this instance's backups (OdooBackup) are written and
	// how they and in-place restores (OdooRestore) run.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	Backup OdooBackupConfig `json:"backup,omitempty"`

	// Bootstrap says how the database is made on first boot. Without it a
	// new database is initialized with spec.modules.
	// +kubebuilder:validation:Optional
	Bootstrap *Bootstrap `json:"bootstrap,omitempty"`
}

// Bootstrap says how an instance's database is made on first boot.
type Bootstrap struct {
	// FromBackup restores a backup instead of initializing a new database:
	// the way an existing Odoo moves onto the operator, or an instance is
	// recreated from its backups. It is used once, before Odoo first
	// starts; later changes are ignored.
	// +kubebuilder:validation:Optional
	FromBackup *BootstrapFromBackup `json:"fromBackup,omitempty"`
}

// BootstrapFromBackup is the backup an instance starts from.
type BootstrapFromBackup struct {
	BackupSource `json:",inline"`
	// Neutralize runs Odoo's neutralize on the restored database before
	// Odoo first starts on it, and gives it a new database.uuid (see
	// OdooRestore spec.neutralize). Set it for a copy of production.
	Neutralize bool `json:"neutralize,omitempty"`
	// Upgrade upgrades the restored database to spec.image before Odoo
	// first starts on it (see OdooRestore spec.upgrade). Set it when the
	// backup was made by other code than spec.image holds, such as a
	// production backup started on a newer version of its modules.
	Upgrade bool `json:"upgrade,omitempty"`
}

// BootstrapStatus records the restore spec.bootstrap.fromBackup started.
type BootstrapStatus struct {
	// Restore is the OdooRestore that restores the backup.
	Restore string `json:"restore,omitempty"`
	// Completed is set once it has restored the backup.
	Completed bool `json:"completed,omitempty"`
}

// MaintenanceJobStatus records the maintenance Job (init or upgrade) the
// operator is waiting for. The JSON keys keep the 0.2.x names.
type MaintenanceJobStatus struct {
	// The name of the Job
	Name string `json:"name,omitempty"`
	// The namespace of the Job
	Namespace string `json:"jobNamespace,omitempty"`
	// Kind is "init" or "upgrade"
	Kind string `json:"kind,omitempty"`
	// Image the Job runs; becomes status.appliedImage on success
	Image string `json:"image,omitempty"`
	// Token is the spec.upgrade.token the Job was created for; becomes status.appliedUpgradeToken on success
	Token string `json:"token,omitempty"`
	// The list of modules that are being installed (-i)
	Modules []string `json:"modules,omitempty"`
	// The list of modules that are being upgraded (-u)
	UpgradeModules []string `json:"upgradeModules,omitempty"`
	// StartedAt is when the operator created the Job.
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
}

// Kinds of status.currentOperation.
const (
	OperationBackup  = "backup"
	OperationRestore = "restore"
	// OperationCleanup deletes the data an OdooRestore replaced.
	OperationCleanup = "cleanup"
	// OperationDrill restores the newest backup into scratch space and checks it.
	OperationDrill = "drill"
)

// OperationStatus records the backup, restore or cleanup Job an instance is
// running. Only one Job (init, upgrade, backup, restore or cleanup) runs per
// instance at a time.
type OperationStatus struct {
	// Kind: backup, restore, cleanup or drill.
	// +kubebuilder:validation:Enum=backup;restore;cleanup;drill
	Kind string `json:"kind,omitempty"`
	// Name of the OdooBackup or OdooRestore.
	Name string `json:"name,omitempty"`
	// Job running it; empty while the operator is still stopping Odoo for it.
	Job string `json:"job,omitempty"`
	// StoppedOdoo is true when the operation scaled Odoo to zero and must
	// bring it back.
	StoppedOdoo bool `json:"stoppedOdoo,omitempty"`
	// StoreReached is true once an offline backup has checked, with Odoo
	// still serving, that its store can be reached.
	StoreReached bool `json:"storeReached,omitempty"`
	// StartedAt is when the operation started.
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
}

// OdooBackupScheduleStatus reports the backup schedule and the last drill.
type OdooBackupScheduleStatus struct {
	// LastScheduleTime is the latest scheduled time the operator has handled,
	// whether it took a backup then or skipped it.
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`
	// NextScheduleTime is when the next scheduled backup is due.
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`
	// Schedule and TimeZone the times above were computed for.
	Schedule string `json:"schedule,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`

	// LastDrillScheduleTime is the latest drill time the operator has handled.
	LastDrillScheduleTime *metav1.Time `json:"lastDrillScheduleTime,omitempty"`
	// NextDrillTime is when the next drill is due.
	NextDrillTime *metav1.Time `json:"nextDrillTime,omitempty"`
	// DrillSchedule the drill times above were computed for.
	DrillSchedule string `json:"drillSchedule,omitempty"`
	// DrillDue is the scheduled time of a drill waiting in the instance's
	// queue.
	DrillDue *metav1.Time `json:"drillDue,omitempty"`
	// LastDrill is the result of the latest restore drill.
	LastDrill *OdooDrillResult `json:"lastDrill,omitempty"`
	// LastSuccessfulDrillTime is when a restore drill last passed.
	LastSuccessfulDrillTime *metav1.Time `json:"lastSuccessfulDrillTime,omitempty"`
}

// OdooDrillResult is the outcome of one restore drill.
type OdooDrillResult struct {
	// Backup is the OdooBackup the drill restored.
	Backup string `json:"backup,omitempty"`
	// SnapshotID is the snapshot the drill restored.
	SnapshotID string `json:"snapshotID,omitempty"`
	// Succeeded is true when the backup restored, passed every check and
	// Odoo read it.
	Succeeded bool `json:"succeeded"`
	// Message explains a failure, or sums up a success.
	Message string `json:"message,omitempty"`
	// StartedAt and CompletedAt bound the drill.
	StartedAt   *metav1.Time `json:"startedAt,omitempty"`
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// Job ran the drill; a failed drill's Job is kept for its log.
	Job string `json:"job,omitempty"`
}

// OdooDatabaseStatus records the database the operator resolved for this OdooDeployment.
type OdooDatabaseStatus struct {
	// Name of the database
	Name string `json:"name,omitempty"`
	// Host the database was resolved on
	Host string `json:"host,omitempty"`
	// ProvisionedBy is "operator" when the operator created the database and
	// "external" when it adopted a pre-existing one. Only operator-provisioned
	// databases are ever dropped.
	// +kubebuilder:validation:Enum=operator;external
	ProvisionedBy string `json:"provisionedBy,omitempty"`
	// State is Provisioning while CREATE DATABASE is in flight and Ready afterwards.
	// +kubebuilder:validation:Enum=Provisioning;Ready
	State string `json:"state,omitempty"`
	// CreatedAt is set when the operator created the database.
	CreatedAt *metav1.Time `json:"createdAt,omitempty"`
}

// OdooDeploymentStatus defines the observed state of OdooDeployment
type OdooDeploymentStatus struct {
	// ObservedGeneration is the spec generation the status reflects.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase summarises the lifecycle: Pending, Initializing, Upgrading, Running, Failed.
	// +kubebuilder:validation:Enum=Pending;Initializing;Upgrading;Running;Failed;BackingUp;Restoring
	Phase string `json:"phase,omitempty"`

	// AppliedImage is the image the Deployment runs (the last image a
	// maintenance Job succeeded with, or spec.image for a plain roll-out).
	AppliedImage string `json:"appliedImage,omitempty"`

	// AppliedUpgradeToken is the spec.upgrade.token the last upgrade ran with.
	AppliedUpgradeToken string `json:"appliedUpgradeToken,omitempty"`

	// Database records the resolved database and who provisioned it.
	Database OdooDatabaseStatus `json:"database,omitempty"`

	// ReadyReplicas mirrors the Deployment's available replicas.
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// The name of the Secret used to store the Odoo configuration file
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=""
	OdooConfigSecretName string `json:"odooConfigSecretName,omitempty"`

	// The name of the PVC used for the Odoo data
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=""
	OdooDataPvcName string `json:"odooDataPvcName,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	InitModulesInstalled []string `json:"initModulesInstalled"`

	// The maintenance Job (init or upgrade) currently running
	// +kubebuilder:validation:Optional
	CurrentInitJob MaintenanceJobStatus `json:"currentInitJob,omitempty"`

	// CurrentOperation is the backup, restore or cleanup in progress.
	// +kubebuilder:validation:Optional
	CurrentOperation OperationStatus `json:"currentOperation,omitempty"`

	// Backup reports the backup schedule and the last restore drill.
	// +kubebuilder:validation:Optional
	Backup OdooBackupScheduleStatus `json:"backup,omitempty"`

	// PreUpgradeBackup is the backup spec.backup.beforeUpgrade takes before
	// the pending upgrade. It is cleared once no upgrade is pending.
	// +kubebuilder:validation:Optional
	PreUpgradeBackup PreUpgradeBackupStatus `json:"preUpgradeBackup,omitempty"`

	// Bootstrap is the restore spec.bootstrap.fromBackup started.
	// +kubebuilder:validation:Optional
	Bootstrap BootstrapStatus `json:"bootstrap,omitempty"`

	// LastMaintenanceDuration is how long the last successful upgrade Job
	// took, from its creation to its completion. The maintenance page uses it
	// to estimate the next one. Init Jobs are not counted: they run once and
	// take far longer than an upgrade.
	// +kubebuilder:validation:Optional
	LastMaintenanceDuration *metav1.Duration `json:"lastMaintenanceDuration,omitempty"`

	// The secret name for the Odoo admin password
	// +kubebuilder:validation:Optional
	OdooAdminSecretName string `json:"odooAdminSecretName,omitempty"`

	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PreUpgradeBackupStatus names the backup taken before an upgrade.
type PreUpgradeBackupStatus struct {
	// Name of the OdooBackup.
	// +kubebuilder:validation:Optional
	Name string `json:"name,omitempty"`
	// Target is the image and upgrade token it was taken for.
	// +kubebuilder:validation:Optional
	Target string `json:"target,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.status.appliedImage`
// +kubebuilder:printcolumn:name="Database",type=string,JSONPath=`.status.database.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OdooDeployment is the Schema for the odoodeployments API
type OdooDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OdooDeploymentSpec   `json:"spec,omitempty"`
	Status OdooDeploymentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OdooDeploymentList contains a list of OdooDeployment
type OdooDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OdooDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OdooDeployment{}, &OdooDeploymentList{})
}
