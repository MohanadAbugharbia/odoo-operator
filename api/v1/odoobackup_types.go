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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LabelBackupKind marks an OdooBackup with its kind: manual (the default),
// scheduled or pre-upgrade.
const LabelBackupKind = "odoo.abugharbia.com/backup-kind"

// AnnotationScheduledTime is the scheduled time, RFC 3339, an OdooBackup
// with kind scheduled was created for.
const AnnotationScheduledTime = "odoo.abugharbia.com/scheduled-time"

// AnnotationUpgradeTarget is the upgrade a pre-upgrade OdooBackup was taken
// for: spec.image and spec.upgrade.token, space-separated.
const AnnotationUpgradeTarget = "odoo.abugharbia.com/upgrade-target"

// Backup kinds, in the LabelBackupKind label and the restic "kind=" tag.
const (
	BackupKindManual     = "manual"
	BackupKindScheduled  = "scheduled"
	BackupKindPreUpgrade = "pre-upgrade"
)

// OdooBackup phases.
const (
	BackupPhasePending                    = "Pending"
	BackupPhaseWaitingForFilestoreCleanup = "WaitingForFilestoreCleanup"
	BackupPhaseRunning                    = "Running"
	BackupPhaseCompleted                  = "Completed"
	BackupPhaseCompletedWithWarnings      = "CompletedWithWarnings"
	BackupPhasePartiallyCompleted         = "PartiallyCompleted"
	BackupPhaseFailed                     = "Failed"
)

// OdooBackupSpec names the instance to back up.
type OdooBackupSpec struct {
	// OdooDeployment to back up, in the same namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.odooDeployment is immutable"
	OdooDeployment string `json:"odooDeployment"`

	// Keep marks a backup that retention never removes.
	// +kubebuilder:validation:Optional
	Keep bool `json:"keep,omitempty"`
}

// OdooBackupStatus describes one backup.
type OdooBackupStatus struct {
	// Phase: Pending, WaitingForFilestoreCleanup, Running, Completed,
	// CompletedWithWarnings, PartiallyCompleted (taken, but a copy to another
	// store failed) or Failed.
	// +kubebuilder:validation:Enum=Pending;WaitingForFilestoreCleanup;Running;Completed;CompletedWithWarnings;PartiallyCompleted;Failed
	Phase string `json:"phase,omitempty"`
	// Message explains the phase in a sentence.
	Message string `json:"message,omitempty"`

	// Store is the OdooBackupStore the backup was written to.
	Store string `json:"store,omitempty"`
	// Repository is the restic repository inside the store, <namespace>/<deployment>.
	Repository string `json:"repository,omitempty"`
	// SnapshotID is the restic snapshot holding this backup.
	SnapshotID string `json:"snapshotID,omitempty"`
	// Consistency the backup was taken with: Online or Offline.
	Consistency string `json:"consistency,omitempty"`

	// Job is the Job taking the backup. Attempts counts the Jobs started for it.
	Job      string `json:"job,omitempty"`
	Attempts int32  `json:"attempts,omitempty"`
	// RetryAfter is when a backup that waited for Odoo's filestore cleanup
	// is tried again.
	RetryAfter *metav1.Time `json:"retryAfter,omitempty"`
	// WaitingSince is when the backup started waiting for Odoo's filestore
	// cleanup to finish.
	WaitingSince *metav1.Time `json:"waitingSince,omitempty"`
	// StartedAt is when the backup Job was created.
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// CompletedAt is when the backup finished, successfully or not.
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Image is the Odoo image the instance ran when the backup was taken.
	Image string `json:"image,omitempty"`
	// OdooVersion is the Odoo series in the database, such as 18.0.
	OdooVersion string `json:"odooVersion,omitempty"`
	// PgServerVersion is the PostgreSQL server version, such as 16.4.
	PgServerVersion string `json:"pgServerVersion,omitempty"`

	// DatabaseBytes is the size of the SQL dump.
	DatabaseBytes int64 `json:"databaseBytes,omitempty"`
	// FilestoreBytes is the size of the filestore.
	FilestoreBytes int64 `json:"filestoreBytes,omitempty"`
	// FilestoreFiles is the number of files in the filestore.
	FilestoreFiles int64 `json:"filestoreFiles,omitempty"`
	// AddedBytes is the new data this backup uploaded after deduplication
	// and compression.
	AddedBytes int64 `json:"addedBytes,omitempty"`
	// RepairedFiles counts attachments that were gone from the filestore
	// when the backup ran and were taken from the previous backup instead.
	RepairedFiles int32 `json:"repairedFiles,omitempty"`
	// MissingFiles counts attachments the database refers to that are in
	// neither the filestore nor the previous backup.
	MissingFiles int32 `json:"missingFiles,omitempty"`
	// MissingFileNames lists the first missing files.
	// +listType=atomic
	MissingFileNames []string `json:"missingFileNames,omitempty"`

	// Copies are the copies of this backup in the instance's other stores.
	// +listType=atomic
	Copies []BackupCopy `json:"copies,omitempty"`

	// Warnings name what went wrong around a backup that was still taken,
	// such as retention that could not run.
	// +listType=atomic
	Warnings []string `json:"warnings,omitempty"`
}

// BackupCopy is one copy of a backup in another store.
type BackupCopy struct {
	// Store is the OdooBackupStore holding the copy.
	Store string `json:"store"`
	// SnapshotID of the copy in that store's repository; empty when the
	// copy failed.
	SnapshotID string `json:"snapshotID,omitempty"`
	// File is the copy in a store of format zip: the Odoo zip's path below
	// the store's root.
	File string `json:"file,omitempty"`
	// Error says why the copy failed.
	Error string `json:"error,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=odoobk
// +kubebuilder:printcolumn:name="Instance",type=string,JSONPath=`.spec.odooDeployment`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Snapshot",type=string,JSONPath=`.status.snapshotID`
// +kubebuilder:printcolumn:name="Store",type=string,JSONPath=`.status.store`
// +kubebuilder:printcolumn:name="Completed",type=date,JSONPath=`.status.completedAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OdooBackup is one full backup of an OdooDeployment: its database and its
// filestore, taken together. Create one to take a backup now. It is not owned
// by the OdooDeployment, so deleting an instance never deletes its backups.
type OdooBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OdooBackupSpec   `json:"spec,omitempty"`
	Status OdooBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OdooBackupList contains a list of OdooBackup
type OdooBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OdooBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OdooBackup{}, &OdooBackupList{})
}
