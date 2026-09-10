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

	// LabelOdooDeployment marks every pod (Deployment and Job) that belongs to an OdooDeployment.
	LabelOdooDeployment = "odoo.abugharbia.com/odoodeployment"
	// LabelJobKind marks maintenance Job pods with "init" or "upgrade".
	LabelJobKind = "odoo.abugharbia.com/job-kind"
	// AnnotationConfigHash carries the sha256 of the rendered odoo.conf on the
	// Deployment pod template so that config changes roll the pods.
	AnnotationConfigHash = "odoo.abugharbia.com/config-hash"

	JobKindInit    = "init"
	JobKindUpgrade = "upgrade"
)

// Condition types.
const (
	ConditionReady         = "Ready"
	ConditionDatabaseReady = "DatabaseReady"
	ConditionInitialized   = "Initialized"
	// ConditionDegraded is True only when something is wrong.
	ConditionDegraded = "Degraded"
)

// Phases.
const (
	PhasePending      = "Pending"
	PhaseInitializing = "Initializing"
	PhaseUpgrading    = "Upgrading"
	PhaseRunning      = "Running"
	PhaseFailed       = "Failed"
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
	// +kubebuilder:validation:Enum=Pending;Initializing;Upgrading;Running;Failed
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

	// The secret name for the Odoo admin password
	// +kubebuilder:validation:Optional
	OdooAdminSecretName string `json:"odooAdminSecretName,omitempty"`

	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
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
