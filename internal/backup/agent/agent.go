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

// Package agent is what runs inside the backup, restore, cleanup and probe
// Jobs. Each entry point returns a result.Result, which the Job writes to its
// termination message for the operator to read.
//
// The rules every function here keeps:
//   - live data is never changed by a backup, and by a restore only through
//     the swap, which a retry always finishes;
//   - only names from package layout are ever dropped or deleted;
//   - every failure says at which step it stopped.
package agent

import (
	"crypto/sha1"
	"encoding/hex"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/odooconf"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/pg"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/restic"
)

// Env is what every Job needs.
type Env struct {
	// Config is the instance's odoo.conf.
	Config odooconf.Config
	// MaintenanceDB is where CREATE, DROP and RENAME DATABASE run.
	MaintenanceDB string
	// Restic runs restic against the instance's repository.
	Restic *restic.Restic
	// Clients finds pg_dump and psql.
	Clients pg.Clients
	// Log receives progress lines (the Job's log).
	Log *log.Logger
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

func (e *Env) now() time.Time {
	if e.Now == nil {
		return time.Now()
	}
	return e.Now()
}

func (e *Env) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log.Printf(format, args...)
	}
}

// NewLogger returns the logger the Jobs use.
func NewLogger(w io.Writer) *log.Logger {
	return log.New(w, "", log.LstdFlags|log.LUTC)
}

// storeNameRE is what Odoo's store_fname looks like (<sha[:2]>/<sha>, or
// older layouts with more folders). Anything else is never used as a path.
var storeNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`)

// safeStoreName reports whether a store_fname is a plain relative path.
func safeStoreName(name string) bool {
	if !storeNameRE.MatchString(name) {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

var sha1NameRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// contentMatchesName checks a filestore file against its name when the name
// is the sha1 of the content, as Odoo names them. Names that are not a sha1
// cannot be checked and pass.
func contentMatchesName(path string) (bool, error) {
	base := filepath.Base(path)
	if !sha1NameRE.MatchString(base) {
		return true, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == base, nil
}

// isFile reports whether path is a regular file.
func isFile(path string) bool {
	st, err := os.Lstat(path)
	return err == nil && st.Mode().IsRegular()
}

// exists reports whether path exists (any type).
func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	}
	return false, err
}

// writeFileAtomic writes data to path through a temporary file and a rename,
// and syncs both, so a crash leaves the old or the new content.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir makes renames in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil && !strings.Contains(err.Error(), "invalid argument") {
		return err
	}
	return nil
}

// sampleIndexes picks up to n indexes spread evenly over 0..total-1.
func sampleIndexes(total, n int) []int {
	if total <= 0 || n <= 0 {
		return nil
	}
	if total <= n {
		out := make([]int, total)
		for i := range out {
			out[i] = i
		}
		return out
	}
	out := make([]int, n)
	for i := range out {
		out[i] = i * total / n
	}
	return out
}

// limit returns at most n entries of list.
func limit(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	return list[:n]
}
