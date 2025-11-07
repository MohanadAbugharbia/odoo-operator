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

// type S3Config struct {
// 	// The S3 endpoint to use for backups
// 	Endpoint string `json:"endpoint,omitempty"`
// 	// The S3 bucket to use for backups
// 	Bucket string `json:"bucket"`
// 	// Prefix to use for backups
// 	Prefix string `json:"prefix,omitempty"`
// 	// The S3 region to use for backups
// 	Region string `json:"region"`
// 	// The S3 access key to use for backups
// 	AccessKeyFromSecret corev1.SecretKeySelector `json:"accessKeyFromSecret"`
// 	// The S3 secret key to use for backups
// 	SecretKeyFromSecret corev1.SecretKeySelector `json:"secretKeyFromSecret"`
// }

// type OdooBackupConfig struct {
// 	// Whether or not to enable backups
// 	// +kubebuilder:default=false
// 	Enabled bool `json:"enabled"`
// 	// The number of daily backups to keep at all times
// 	// +kubebuilder:default=7
// 	KeepDailyBackups int32 `json:"keepDailyBackups,omitempty"`
// 	// The number of weekly backups to keep at all times
// 	// +kubebuilder:default=4
// 	KeepWeeklyBackups int32 `json:"keepWeeklyBackups,omitempty"`
// 	// The number of monthly backups to keep at all times
// 	// +kubebuilder:default=12
// 	KeepMonthlyBackups int32 `json:"keepMonthlyBackups,omitempty"`
// 	// The S3 configuration for the OdooDployment
// 	S3 S3Config `json:"s3,omitempty"`
// }

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

	// The database name to use for Odoo
	// +kubebuilder:default="odoo"
	Name string `json:"name,omitempty"`
	// The database name to use for Odoo from a secret
	NameFromSecret corev1.SecretKeySelector `json:"nameFromSecret,omitempty"`

	// Whether or not to enable SSL for the database connection
	// +kubebuilder:default=false
	SSL bool `json:"ssl,omitempty"`
	// Whether or not to enable SSL for the database connection from a secret
	SSLFromSecret corev1.SecretKeySelector `json:"sslFromSecret,omitempty"`

	// The database max connections to use for Odoo
	// +kubebuilder:default=20
	MaxConn int32 `json:"maxConn,omitempty"`
	// The database max connections to use for Odoo from a secret
	MaxConnFromSecret corev1.SecretKeySelector `json:"maxConnFromSecret,omitempty"`
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
	WithoutDemo bool `json:"withoutDemo,omitempty"`

	// Proxy Mode for Odoo
	// This instructs Odoo to use the X-Forwarded-For header for the remote IP address
	// +kubebuilder:default=true
	ProxyMode bool `json:"proxyMode,omitempty"`

	// Numer of process workers to use for Odoo
	// +kubebuilder:default=2
	Workers int32 `json:"workers,omitempty"`

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
}

type PersistentVolumeClaimSpec struct {
	// Name of the persistent volume claim
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="odoo-filestore"
	Name string `json:"name,omitempty"`

	// CreateIfNotExists defines if the persistent volume claim should be created in case not found
	// +kubebuilder:default=true
	CreateIfNotExists bool `json:"createIfNotExists,omitempty"`

	// StorageSize defines the size of the new persistent volume claim
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="10Gi"
	Size resource.Quantity `json:"size,omitempty"`

	// StorageClass is the storageClassName used to create a new persistent volume claim
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=default
	StorageClassName string `json:"storageClassName,omitempty"`
	// AccessMode defines the access mode of the new persistent volume claim
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={"ReadWriteOnce"}
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`
}

// OdooDeploymentSpec defines the desired state of OdooDeployment
type OdooDeploymentSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// The name of the OdooDployment
	Name string `json:"name"`
	// The number of replicas to run for the OdooDployment
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Replicas int32 `json:"replicas,omitempty"`
	// The image to run for the OdooDployment
	// +kubebuilder:default="odoo:18"
	Image string `json:"image,omitempty"`
	// The backup configuration for the OdooDployment

	// Image pull policy for the OdooDployment
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="IfNotPresent"
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// Backup OdooBackupConfig `json:"backup,omitempty"`
	// The database configuration for the OdooDployment
	Database OdooDatabaseConfig `json:"database,omitempty"`
	// The configuration for the Odoo
	Config OdooConfig `json:"config,omitempty"`

	// A list of modules to initialise the database with
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:default={"base"}
	Modules []string `json:"modules,omitempty"`

	// PersistentVolumeClaim defines the replicated volume specs
	// +kubebuilder:validation:Optional
	OdooFilestore PersistentVolumeClaimSpec `json:"odooFilestore,omitempty"`
}

type DBInitjob struct {
	// The name of the InitJob
	Name string `json:"name"`
	// The name of the InitJob
	Namespace string `json:"jobNamespace"`

	// The list of modules that are being installed
	Modules []string `json:"modules,omitempty"`
}

// OdooDeploymentStatus defines the observed state of OdooDeployment
type OdooDeploymentStatus struct { // INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

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

	// The name of the current running InitJob
	// +kubebuilder:validation:Optional
	CurrentInitJob DBInitjob `json:"currentInitJob,omitempty"`

	// The secret name for the Odoo admin password
	// +kubebuilder:validation:Optional
	OdooAdminSecretName string `json:"odooAdminSecretName,omitempty"`

	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"conditions"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

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
