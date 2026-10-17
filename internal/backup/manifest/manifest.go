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

// Package manifest reads and writes manifest.json: the keys Odoo's own
// backups carry (odoo/service/db.py, dump_db_manifest) plus an "operator"
// key with what the operator needs to restore and verify a backup. Odoo's
// restore ignores keys it does not know, so an operator backup stays a valid
// Odoo backup.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Manifest is manifest.json.
type Manifest struct {
	OdooDump     string            `json:"odoo_dump"`
	DBName       string            `json:"db_name"`
	Version      string            `json:"version"`
	VersionInfo  []any             `json:"version_info"`
	MajorVersion string            `json:"major_version"`
	PgVersion    string            `json:"pg_version"`
	Modules      map[string]string `json:"modules"`

	// Operator is absent from backups Odoo made itself.
	Operator *Operator `json:"operator,omitempty"`
}

// Operator is the operator's own part of the manifest.
type Operator struct {
	// Version of the backup tool that wrote the backup.
	Version string `json:"version"`
	// Namespace, Deployment and Backup name the instance and the OdooBackup.
	Namespace  string `json:"namespace"`
	Deployment string `json:"deployment"`
	Backup     string `json:"backup"`
	// Kind: manual, scheduled or pre-upgrade.
	Kind string `json:"kind"`
	// Image and UpgradeToken are the instance's status.appliedImage and
	// status.appliedUpgradeToken: a restore starts Odoo on them.
	Image        string `json:"image,omitempty"`
	UpgradeToken string `json:"upgradeToken,omitempty"`
	// Consistency: Online or Offline.
	Consistency string `json:"consistency"`
	// SnapshotTime is when the database snapshot was taken.
	SnapshotTime time.Time `json:"snapshotTime"`
	// PgServerVersionNum is server_version_num, PgDump the pg_dump version.
	PgServerVersionNum int    `json:"pgServerVersionNum"`
	PgDump             string `json:"pgDump"`
	// DumpSHA256 and DumpBytes describe dump.sql.
	DumpSHA256 string `json:"dumpSha256"`
	DumpBytes  int64  `json:"dumpBytes"`
	// RowCounts are the rows of every table in the snapshot, by
	// schema-qualified name. A restore must find exactly these.
	RowCounts map[string]int64 `json:"rowCounts"`
	// Files are the filestore checks.
	Files Files `json:"files"`
}

// Files is what the backup found in the filestore.
type Files struct {
	// Referenced is how many attachments the database refers to.
	Referenced int `json:"referenced"`
	// Repaired attachments were gone from the filestore and were taken from
	// the previous backup; they are under filestore-repaired/.
	Repaired []string `json:"repaired,omitempty"`
	// Missing attachments are in neither.
	Missing []string `json:"missing,omitempty"`
}

// Series returns the Odoo series of a module version ("18.0.1.3" → "18.0",
// "saas~18.2.1.0" → "saas~18.2"), or "" when it is not one.
func Series(moduleVersion string) string {
	parts := strings.Split(moduleVersion, ".")
	if len(parts) < 2 {
		return ""
	}
	major := strings.TrimPrefix(parts[0], "saas~")
	if _, err := strconv.Atoi(major); err != nil {
		return ""
	}
	if _, err := strconv.Atoi(parts[1]); err != nil {
		return ""
	}
	return parts[0] + "." + parts[1]
}

// VersionInfo is Odoo's release.version_info for a series.
func VersionInfo(series string) []any {
	parts := strings.SplitN(series, ".", 2)
	if len(parts) != 2 {
		return nil
	}
	var first any = parts[0]
	if n, err := strconv.Atoi(parts[0]); err == nil {
		first = n
	}
	minor, _ := strconv.Atoi(parts[1])
	return []any{first, minor, 0, "final", 0, ""}
}

// PgVersion formats server_version_num the way Odoo's manifest does.
func PgVersion(serverVersionNum int) string {
	v := serverVersionNum / 100
	return fmt.Sprintf("%d.%d", v/100, v%100)
}

// ServerVersion formats server_version_num as PostgreSQL prints it: 16.4
// for 160004, 9.6.24 for 90624.
func ServerVersion(serverVersionNum int) string {
	if serverVersionNum >= 100000 {
		return fmt.Sprintf("%d.%d", serverVersionNum/10000, serverVersionNum%10000)
	}
	return fmt.Sprintf("%d.%d.%d", serverVersionNum/10000, serverVersionNum/100%100, serverVersionNum%100)
}

// New returns the Odoo part of a manifest for a database.
func New(dbName string, modules map[string]string, serverVersionNum int) Manifest {
	series := Series(modules["base"])
	return Manifest{
		OdooDump:     "1",
		DBName:       dbName,
		Version:      series,
		VersionInfo:  VersionInfo(series),
		MajorVersion: series,
		PgVersion:    PgVersion(serverVersionNum),
		Modules:      modules,
	}
}

// Series is the Odoo series the backup was taken on: major_version, or
// what the base module's version says when major_version is missing.
func (m *Manifest) Series() string {
	if m.MajorVersion != "" {
		return m.MajorVersion
	}
	if s := Series(m.Modules["base"]); s != "" {
		return s
	}
	return m.Version
}

// Encode returns the manifest as indented JSON, as Odoo writes it.
func (m *Manifest) Encode() ([]byte, error) {
	return json.MarshalIndent(m, "", "    ")
}

// ErrNotOdoo is returned for a JSON file that is not an Odoo manifest.
var ErrNotOdoo = errors.New("not an Odoo backup manifest")

// Parse reads manifest.json.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("manifest.json: %w", err)
	}
	if m.OdooDump == "" && m.Operator == nil {
		return Manifest{}, ErrNotOdoo
	}
	return m, nil
}

// CheckSeries refuses to restore a backup taken on another Odoo series than
// the target's: Odoo cannot update a database across major versions.
func CheckSeries(backup, target string) error {
	switch {
	case target == "":
		return nil
	case backup == "":
		return fmt.Errorf("the backup does not say which Odoo version it was taken on; the instance runs %s", target)
	case backup != target:
		return fmt.Errorf("the backup was taken on Odoo %s but the instance runs Odoo %s; "+
			"a backup can only be restored on the same Odoo version", backup, target)
	}
	return nil
}
