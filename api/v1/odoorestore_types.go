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
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OdooRestore phases.
const (
	RestorePhasePending   = "Pending"
	RestorePhaseStopping  = "Stopping"
	RestorePhaseRestoring = "Restoring"
	RestorePhaseCompleted = "Completed"
	RestorePhaseFailed    = "Failed"
)

// States of the data an OdooRestore replaced.
const (
	// PreviousKept: the replaced database and files are kept under their
	// _prev names until keptUntil.
	PreviousKept = "Kept"
	// PreviousRemoved: keepPrevious passed and they were deleted.
	PreviousRemoved = "Removed"
	// PreviousUndone: an undo put them back in place.
	PreviousUndone = "Undone"
	// PreviousNone: there was no live database to keep.
	PreviousNone = "None"
)

// RestoreFinalizer keeps an OdooRestore until its Job has finished and the
// data it replaced has been deleted.
const RestoreFinalizer = "odoo.abugharbia.com/restore"

// BackupSource is a backup that is not one of the instance's own
// OdooBackups: an Odoo backup file (.zip or .dump, as Odoo's database
// manager makes them) or a snapshot in any restic repository, in an
// OdooBackupStore of the same namespace.
// +kubebuilder:validation:XValidation:rule="!has(self.path) || !(self.path.endsWith('.zip') || self.path.endsWith('.dump')) || (!has(self.snapshot) && !has(self.host) && !has(self.tags))",message="snapshot, host and tags are for restic repositories, not for .zip or .dump files"
// +kubebuilder:validation:XValidation:rule="!has(self.path) || self.path.endsWith('.zip') || self.path.endsWith('.dump') || !(self.path.contains('*') || self.path.contains('?') || self.path.contains('['))",message="only a .zip or .dump path may be a pattern"
type BackupSource struct {
	// Store is the OdooBackupStore that holds the backup.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Store string `json:"store"`
	// Path inside the store, below its path (or subPath). A path ending in
	// .zip or .dump is an Odoo backup file; its last segment may be a
	// pattern (*, ?, [a-z]), and the match whose name sorts last, the
	// newest for dated names, is restored. Any other path is a restic
	// repository folder; empty is this instance's own repository
	// (<namespace>/<name>).
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^([A-Za-z0-9._~+@=-]+/)*[A-Za-z0-9._~+@=*?\[\]-]+$`
	// +kubebuilder:validation:XValidation:rule="!self.split('/').exists(s, s == '..' || s == '.')",message="path segments cannot be . or .."
	Path string `json:"path,omitempty"`
	// Snapshot in a restic repository: its ID, or latest (default) for the
	// newest snapshot with Host and Tags.
	// +kubebuilder:validation:Pattern=`^(latest|[0-9a-f]{8,64})$`
	Snapshot string `json:"snapshot,omitempty"`
	// Host picks latest among the snapshots of one restic --host, such as
	// <namespace>/<name> for the operator's own snapshots.
	// +kubebuilder:validation:MaxLength=253
	Host string `json:"host,omitempty"`
	// Tags picks latest among the snapshots that have all these restic
	// tags, such as deployment=<namespace>/<name>.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=10
	// +kubebuilder:validation:items:MaxLength=253
	Tags []string `json:"tags,omitempty"`
}

// IsFile reports whether the source is an Odoo backup file rather than a
// restic repository.
func (s *BackupSource) IsFile() bool {
	return s != nil && (strings.HasSuffix(s.Path, ".zip") || strings.HasSuffix(s.Path, ".dump"))
}

// SnapshotValue is the snapshot to restore from a restic repository.
func (s *BackupSource) SnapshotValue() string {
	if s.Snapshot == "" {
		return "latest"
	}
	return s.Snapshot
}

// OdooRestoreSpec says which backup replaces an instance's data.
// +kubebuilder:validation:XValidation:rule="(has(self.backup) ? 1 : 0) + (has(self.snapshotID) ? 1 : 0) + (has(self.undo) ? 1 : 0) + (has(self.from) ? 1 : 0) == 1",message="set exactly one of backup, snapshotID, from, undo"
// +kubebuilder:validation:XValidation:rule="(has(self.backup) == has(oldSelf.backup)) && (!has(self.backup) || self.backup == oldSelf.backup) && (has(self.snapshotID) == has(oldSelf.snapshotID)) && (!has(self.snapshotID) || self.snapshotID == oldSelf.snapshotID) && (has(self.undo) == has(oldSelf.undo)) && (!has(self.undo) || self.undo == oldSelf.undo) && (has(self.from) == has(oldSelf.from)) && (!has(self.from) || self.from == oldSelf.from) && (has(self.neutralize) ? self.neutralize : false) == (has(oldSelf.neutralize) ? oldSelf.neutralize : false) && (has(self.upgrade) ? self.upgrade : false) == (has(oldSelf.upgrade) ? oldSelf.upgrade : false)",message="backup, snapshotID, from, undo, neutralize and upgrade are immutable"
// +kubebuilder:validation:XValidation:rule="!has(self.neutralize) || !self.neutralize || !has(self.undo)",message="an undo puts the replaced data back as it was; neutralize cannot be set"
// +kubebuilder:validation:XValidation:rule="!has(self.upgrade) || !self.upgrade || !has(self.undo)",message="an undo puts the replaced data back as it was; upgrade cannot be set"
type OdooRestoreSpec struct {
	// OdooDeployment whose database and filestore are replaced, in the same namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.odooDeployment is immutable"
	OdooDeployment string `json:"odooDeployment"`

	// Backup is the OdooBackup to restore.
	// +kubebuilder:validation:MinLength=1
	Backup string `json:"backup,omitempty"`
	// SnapshotID is a restic snapshot in the instance's own repository on
	// its first store (spec.backup.stores[0]), for a backup that has no
	// OdooBackup any more.
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{8,64}$`
	SnapshotID string `json:"snapshotID,omitempty"`
	// Undo reverses the OdooRestore of this name: the database and files it
	// replaced go back in place, and the ones it restored are kept as the
	// previous data of this restore. Refused once that data has been
	// removed (keepPrevious passed) or when another restore ran on the
	// instance since.
	// +kubebuilder:validation:MinLength=1
	Undo string `json:"undo,omitempty"`
	// From is a backup in any store: an Odoo .zip or .dump file, or a
	// snapshot in another restic repository, such as another instance's
	// or another cluster's.
	// +kubebuilder:validation:Optional
	From *BackupSource `json:"from,omitempty"`

	// Neutralize runs Odoo's neutralize on the restored database before it
	// is swapped in, and gives it a new database.uuid: scheduled actions,
	// outgoing mail servers, payment providers and similar integrations
	// are turned off, so a copy of production cannot act as production.
	// It runs with the instance's image.
	Neutralize bool `json:"neutralize,omitempty"`

	// Upgrade upgrades the restored database to the instance's spec.image
	// before it is swapped in: Odoo runs -u with spec.upgrade.modules (all
	// modules when that is empty), after neutralize. Set it when the backup
	// was made by other code than the instance runs, such as a backup from
	// another Odoo: a backup from elsewhere otherwise counts as already
	// upgraded. The instance's Odoo must be the backup's series. A failed
	// upgrade fails the restore and leaves the live data as it was.
	Upgrade bool `json:"upgrade,omitempty"`

	// KeepPrevious is how long the replaced database and files are kept
	// for an undo. Overrides spec.backup.restore.keepPrevious.
	// +kubebuilder:validation:Optional
	KeepPrevious *metav1.Duration `json:"keepPrevious,omitempty"`
}

// RestorePrevious is the data an OdooRestore replaced.
type RestorePrevious struct {
	// State: Kept, Removed, Undone or None.
	State string `json:"state,omitempty"`
	// Database is the replaced database's name while it is kept.
	Database string `json:"database,omitempty"`
	// Filestore is the replaced filestore folder, under <dataDir>/filestore/.
	Filestore string `json:"filestore,omitempty"`
	// KeptUntil is when the replaced data is deleted.
	KeptUntil *metav1.Time `json:"keptUntil,omitempty"`
	// UndoneBy is the OdooRestore that put the data back.
	UndoneBy string `json:"undoneBy,omitempty"`
	// Image and UpgradeToken are what the instance ran on the replaced
	// data; an undo starts Odoo on them again.
	Image        string `json:"image,omitempty"`
	UpgradeToken string `json:"upgradeToken,omitempty"`
}

// OdooRestoreStatus describes the restore.
type OdooRestoreStatus struct {
	// Phase: Pending, Stopping (Odoo is being stopped), Restoring,
	// Completed or Failed.
	// +kubebuilder:validation:Enum=Pending;Stopping;Restoring;Completed;Failed
	Phase string `json:"phase,omitempty"`
	// Message explains the phase in a sentence.
	Message string `json:"message,omitempty"`

	// Suffix names the database and filestore folders of this restore:
	// <db>_restore_<suffix> while restoring, <db>_prev_<suffix> for the
	// data it replaced.
	Suffix string `json:"suffix,omitempty"`
	// Store and SnapshotID are the backup that was restored; File is the
	// Odoo backup file restored from Store instead of a snapshot.
	Store      string `json:"store,omitempty"`
	SnapshotID string `json:"snapshotID,omitempty"`
	File       string `json:"file,omitempty"`
	// Image is the Odoo image recorded in the backup; the instance is
	// started on it.
	Image string `json:"image,omitempty"`
	// OdooVersion is the Odoo series in the backup.
	OdooVersion string `json:"odooVersion,omitempty"`

	// Job is the Job running the restore. Attempts counts the Jobs that
	// ran it.
	Job      string `json:"job,omitempty"`
	Attempts int32  `json:"attempts,omitempty"`
	// LiveChanged is true once the restore may have renamed the live
	// database or files; from then on it is finished by retrying, never
	// abandoned.
	LiveChanged bool `json:"liveChanged,omitempty"`
	// LiveChangeUnconfirmed is true while liveChanged rests only on
	// attempts that ended without a result after their main container may
	// have started (it ran, or the pods that would tell are gone).
	// The next attempt reads the restore journal: when it reports that the
	// swap never started, the restore fails with the live data unchanged
	// instead of being retried.
	LiveChangeUnconfirmed bool `json:"liveChangeUnconfirmed,omitempty"`
	// RetryAfter is when the next attempt (of the restore or its cleanup)
	// starts after a failed one.
	RetryAfter *metav1.Time `json:"retryAfter,omitempty"`

	StartedAt   *metav1.Time `json:"startedAt,omitempty"`
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Previous is the data this restore replaced.
	Previous RestorePrevious `json:"previous,omitempty"`
	// CleanedUp is true once the cleanup Job deleted what the restore left
	// behind (the replaced data once keepPrevious passed, any half-restored
	// copy, its journal). CleanupAttempts counts its Jobs.
	CleanedUp       bool  `json:"cleanedUp,omitempty"`
	CleanupAttempts int32 `json:"cleanupAttempts,omitempty"`

	// Warnings found while checking the restored data.
	// +listType=atomic
	Warnings []string `json:"warnings,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Instance",type=string,JSONPath=`.spec.odooDeployment`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Backup",type=string,JSONPath=`.spec.backup`
// +kubebuilder:printcolumn:name="Previous",type=string,JSONPath=`.status.previous.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OdooRestore replaces an OdooDeployment's database and filestore with a
// backup, in place. Odoo is stopped while it runs; the replaced data is kept
// for keepPrevious so the restore can be undone.
type OdooRestore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OdooRestoreSpec   `json:"spec,omitempty"`
	Status OdooRestoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OdooRestoreList contains a list of OdooRestore
type OdooRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OdooRestore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OdooRestore{}, &OdooRestoreList{})
}
