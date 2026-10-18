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

// Package layout holds the names and paths the operator and the backup Jobs
// agree on: where a backup lives inside a snapshot, how a restore names the
// databases and filestore folders it creates, and the tags on a snapshot.
// Everything that deletes data checks its target against these names, so the
// operator only ever deletes what it wrote.
package layout

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Paths inside a snapshot and inside the backup Job's pod.
const (
	// BackupRoot is the folder every snapshot holds, in Odoo's zip layout.
	BackupRoot = "/backup"
	// DumpFile is the plain SQL dump, as Odoo's own backups name it.
	DumpFile = "dump.sql"
	// ManifestFile is Odoo's manifest plus the operator's own key.
	ManifestFile = "manifest.json"
	// FilestoreDir holds the filestore, <sha[:2]>/<sha>.
	FilestoreDir = "filestore"
	// RepairedDir holds attachments that were gone from the filestore when
	// the backup ran and were taken from the previous backup. A restore puts
	// them back into the filestore.
	RepairedDir = "filestore-repaired"

	// DataMount is where the restore and cleanup Jobs mount the root of the
	// instance's data PVC (it holds filestore/ and sessions/).
	DataMount = "/data"
	// OperatorDir is the operator's own folder in the data PVC, for restore
	// journals and staging.
	OperatorDir = ".odoo-operator"
	// StoreMount is where an nfs or pvc store is mounted.
	StoreMount = "/store"
	// ConfigMount holds odoo.conf in the Jobs.
	ConfigMount = "/etc/odoo"
	// SecretsMount holds the store's Secrets in the Jobs.
	SecretsMount = "/secrets"
	// TerminationLog is where a Job writes its Result.
	TerminationLog = "/dev/termination-log"
)

// SnapshotPath returns the absolute path of name inside a snapshot.
func SnapshotPath(name ...string) string {
	return path.Join(append([]string{BackupRoot}, name...)...)
}

// Restic snapshot tags and host.
const (
	TagDeployment = "deployment="
	TagBackup     = "backup="
	TagKind       = "kind="
)

// Host is the restic --host of an instance's snapshots.
func Host(namespace, deployment string) string {
	return namespace + "/" + deployment
}

// Tags are the tags of a backup's snapshot.
func Tags(namespace, deployment, backup, kind string) []string {
	return []string{
		TagDeployment + Host(namespace, deployment),
		TagBackup + backup,
		TagKind + kind,
	}
}

// Repository is an instance's repository folder inside a store,
// <namespace>/<deployment>.
func Repository(namespace, deployment string) string {
	return namespace + "/" + deployment
}

// JoinStorePath joins a store path and a repository folder, keeping a
// leading "/" (absolute SFTP paths) and dropping empty segments.
func JoinStorePath(storePath, repo string) string {
	abs := strings.HasPrefix(storePath, "/")
	joined := strings.Trim(path.Join(storePath, repo), "/")
	if abs {
		return "/" + joined
	}
	return joined
}

// SuffixFormat formats the time a restore started into its suffix.
const SuffixFormat = "20060102T150405"

var suffixRE = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}$`)

// Suffix is the suffix of a restore started at t.
func Suffix(t time.Time) string {
	return t.UTC().Format(SuffixFormat)
}

// ValidSuffix reports whether s is a suffix Suffix made.
func ValidSuffix(s string) bool {
	return suffixRE.MatchString(s)
}

// maxIdentifier is PostgreSQL's NAMEDATALEN-1.
const maxIdentifier = 63

const (
	restoreInfix = "_restore_"
	prevInfix    = "_prev_"
)

// base trims db so that db + the longest infix + a suffix fits a
// PostgreSQL identifier, never cutting a UTF-8 character in two (PostgreSQL
// refuses a name that is not valid UTF-8).
func base(db string) string {
	limit := maxIdentifier - len(restoreInfix) - len(SuffixFormat)
	if len(db) <= limit {
		return db
	}
	for limit > 0 && !utf8.RuneStart(db[limit]) {
		limit--
	}
	return db[:limit]
}

// RestoreName is the database (and filestore folder) a restore loads the
// backup into before it swaps it in.
func RestoreName(db, suffix string) string {
	return base(db) + restoreInfix + suffix
}

// PrevName is the name the replaced database (and filestore folder) keeps
// after a restore.
func PrevName(db, suffix string) string {
	return base(db) + prevInfix + suffix
}

// JournalFile is the restore's journal, relative to the data PVC root.
func JournalFile(suffix string) string {
	return path.Join(OperatorDir, "restore-"+suffix+".json")
}

// StagingDir is where a restore unpacks the snapshot, relative to the data
// PVC root (the same volume as the filestore, so moving it in is a rename).
func StagingDir(suffix string) string {
	return path.Join(OperatorDir, "staging-"+suffix)
}

// FilestorePath is a database's filestore folder relative to the data PVC root.
func FilestorePath(db string) string {
	return path.Join(FilestoreDir, db)
}

// CheckName refuses a database or folder name that is not one of the names
// above for db and suffix. Every delete goes through it.
func CheckName(name, db, suffix string) error {
	if !ValidSuffix(suffix) {
		return fmt.Errorf("invalid restore suffix %q", suffix)
	}
	if name == db {
		// A live database can itself be named like a restore copy.
		return fmt.Errorf("%q is the live database", name)
	}
	if name == RestoreName(db, suffix) || name == PrevName(db, suffix) {
		return nil
	}
	return fmt.Errorf("%q is not a name the operator creates for database %q and suffix %s", name, db, suffix)
}
