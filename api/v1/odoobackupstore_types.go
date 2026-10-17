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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BackupStoreFormat is how backups are kept in a store.
// +kubebuilder:validation:Enum=restic
type BackupStoreFormat string

const (
	// BackupStoreFormatRestic keeps one encrypted, deduplicated restic
	// repository per OdooDeployment.
	BackupStoreFormatRestic BackupStoreFormat = "restic"
)

// Keys the operator reads from the Secrets an OdooBackupStore names.
const (
	S3AccessKeyIDKey     = "AWS_ACCESS_KEY_ID"
	S3SecretAccessKeyKey = "AWS_SECRET_ACCESS_KEY"
	SSHPrivateKeyKey     = "id_ed25519"
	SSHKnownHostsKey     = "known_hosts"
)

// StorePath is a folder inside a store: relative segments separated by "/",
// never "..". A leading "/" makes an SFTP path absolute; elsewhere it is
// ignored.
// +kubebuilder:validation:MaxLength=512
// +kubebuilder:validation:Pattern=`^/?([A-Za-z0-9._~+@=-]+(/[A-Za-z0-9._~+@=-]+)*/?)?$`
// +kubebuilder:validation:XValidation:rule="!self.split('/').exists(s, s == '..' || s == '.')",message="path segments cannot be . or .."
type StorePath string

// S3Store is an S3 bucket, on AWS or any S3-compatible service.
type S3Store struct {
	// Endpoint of an S3-compatible service, such as https://s3.example.com.
	// Leave it out for AWS.
	// +kubebuilder:validation:Pattern=`^https?://[^/\s]+/?$`
	Endpoint string `json:"endpoint,omitempty"`
	// Region of the bucket. Optional for most S3-compatible services.
	Region string `json:"region,omitempty"`
	// Bucket holding the backups.
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`
	Bucket string `json:"bucket"`
	// Path inside the bucket. Repositories go under <path>/<namespace>/<deployment>/.
	Path StorePath `json:"path,omitempty"`
	// ForcePathStyle addresses the bucket as <endpoint>/<bucket> instead of
	// <bucket>.<endpoint>. Most S3-compatible services need it; AWS does not.
	ForcePathStyle bool `json:"forcePathStyle,omitempty"`
	// CredentialsSecret holds AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY.
	CredentialsSecret corev1.LocalObjectReference `json:"credentialsSecret"`
}

// SFTPStore is a folder on an SSH server.
type SFTPStore struct {
	// Host of the SSH server.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9.:\[\]-]+$`
	Host string `json:"host"`
	// Port of the SSH server.
	// +kubebuilder:default=22
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`
	// User to log in as.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9._-]+$`
	User string `json:"user"`
	// Path on the server, relative to the user's home folder unless it
	// starts with "/". Repositories go under <path>/<namespace>/<deployment>/.
	Path StorePath `json:"path,omitempty"`
	// SSHSecret holds the private key (id_ed25519) and the server's host key
	// (known_hosts). The host key is always checked.
	SSHSecret corev1.LocalObjectReference `json:"sshSecret"`
}

// NFSStore is an NFS export, mounted into the backup Jobs. The nodes need an
// NFS client and a route to the server.
type NFSStore struct {
	// Server is the NFS server's host name or address.
	// +kubebuilder:validation:MinLength=1
	Server string `json:"server"`
	// Path is the exported folder. Repositories go under <path>/<namespace>/<deployment>/.
	// +kubebuilder:validation:Pattern=`^/`
	Path string `json:"path"`
}

// PVCStore is a PersistentVolumeClaim in the store's namespace, mounted into
// the backup Jobs. A ReadWriteOnce claim only works when the Jobs can run on
// the node that has it attached.
type PVCStore struct {
	// ClaimName of the PersistentVolumeClaim.
	// +kubebuilder:validation:MinLength=1
	ClaimName string `json:"claimName"`
	// SubPath is a folder inside the claim. Repositories go under <subPath>/<namespace>/<deployment>/.
	SubPath StorePath `json:"subPath,omitempty"`
}

// OdooBackupStoreSpec is where backups are written to and read from.
// +kubebuilder:validation:XValidation:rule="(has(self.s3) ? 1 : 0) + (has(self.sftp) ? 1 : 0) + (has(self.nfs) ? 1 : 0) + (has(self.pvc) ? 1 : 0) == 1",message="set exactly one of s3, sftp, nfs, pvc"
type OdooBackupStoreSpec struct {
	// Format is how backups are kept. restic: one encrypted, deduplicated
	// repository per OdooDeployment at <path>/<namespace>/<deployment>/.
	// +kubebuilder:default=restic
	Format BackupStoreFormat `json:"format,omitempty"`

	// S3 stores backups in an S3 bucket.
	S3 *S3Store `json:"s3,omitempty"`
	// SFTP stores backups on an SSH server.
	SFTP *SFTPStore `json:"sftp,omitempty"`
	// NFS stores backups on an NFS export.
	NFS *NFSStore `json:"nfs,omitempty"`
	// PVC stores backups on a PersistentVolumeClaim.
	PVC *PVCStore `json:"pvc,omitempty"`

	// EncryptionKeySecret is the restic password that encrypts every
	// backup in the store. Without it no backup can be read: keep a copy
	// outside the cluster.
	EncryptionKeySecret corev1.SecretKeySelector `json:"encryptionKeySecret"`

	// Resources of the Jobs that probe the store.
	// +kubebuilder:validation:Optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// OdooBackupStoreStatus reports whether the store can be reached.
type OdooBackupStoreStatus struct {
	// ObservedGeneration is the generation the status describes.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Ready is true when the last probe could write to, read from and
	// delete a file in the store.
	// +optional
	Ready bool `json:"ready"`
	// LastProbeTime is when the store was last probed.
	LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`
	// ProbeJob is the Job probing the store right now.
	ProbeJob string `json:"probeJob,omitempty"`
	// ProbedHash identifies the spec and Secrets the last probe ran with.
	ProbedHash string `json:"probedHash,omitempty"`
	// Conditions: Ready.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Format",type=string,JSONPath=`.spec.format`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OdooBackupStore is a place backups are kept: an S3 bucket, an SFTP server,
// an NFS export or a PersistentVolumeClaim. OdooDeployments in the same
// namespace name it in spec.backup.stores.
type OdooBackupStore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OdooBackupStoreSpec   `json:"spec,omitempty"`
	Status OdooBackupStoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OdooBackupStoreList contains a list of OdooBackupStore
type OdooBackupStoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OdooBackupStore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OdooBackupStore{}, &OdooBackupStoreList{})
}
