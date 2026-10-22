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

package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/manifest"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/pg"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/restic"
)

// A backupSource is where a restore reads a backup from: a snapshot in a restic
// repository, or an Odoo backup file (.zip or .dump) in a store.
type backupSource interface {
	// describe names the backup for the log and for messages.
	describe() string
	// manifest is the backup's manifest.json; a backup without one (a
	// .dump file) gives an empty Manifest.
	manifest(ctx context.Context) (manifest.Manifest, error)
	// loadDatabase loads the backup's database into database.
	loadDatabase(ctx context.Context, r *restoreRun, database string, m manifest.Manifest) error
	// filestore unpacks the filestore under staging and returns its
	// folder, plus the folder of repaired attachments ("" when there are
	// none). A missing folder means the backup holds no files.
	filestore(ctx context.Context, staging string) (files, repaired string, err error)
	// warnings are what the source itself found worth saying.
	warnings() []string
	close()
}

// ---- restic -----------------------------------------------------------------

type resticSource struct {
	e    *Env
	snap restic.Snapshot
	root string
}

// openResticSource finds the snapshot: an ID, or "latest" (with host and
// tags) for the newest one.
func openResticSource(ctx context.Context, e *Env, id, host string, tags []string) (*resticSource, error) {
	var (
		snap restic.Snapshot
		err  error
	)
	if id == "" || id == "latest" {
		snap, err = e.Restic.Latest(ctx, host, tags...)
	} else {
		snap, err = e.Restic.Snapshot(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	if len(snap.Paths) != 1 {
		return nil, final(fmt.Errorf("snapshot %s holds %d paths; an Odoo backup holds one", snap.ShortID, len(snap.Paths)))
	}
	return &resticSource{e: e, snap: snap, root: snap.Paths[0]}, nil
}

func (s *resticSource) describe() string   { return "snapshot " + s.snap.ShortID }
func (s *resticSource) warnings() []string { return nil }
func (s *resticSource) close()             {}

func (s *resticSource) manifest(ctx context.Context) (manifest.Manifest, error) {
	var buf bytes.Buffer
	if err := s.e.Restic.Dump(ctx, s.snap.ID, filepath.Join(s.root, layout.ManifestFile), &buf); err != nil {
		return manifest.Manifest{}, fmt.Errorf("read manifest.json: %w", err)
	}
	return manifest.Parse(buf.Bytes())
}

func (s *resticSource) loadDatabase(ctx context.Context, r *restoreRun, database string, m manifest.Manifest) error {
	return r.loadSQL(ctx, database, m, func(ctx context.Context, w io.Writer) error {
		if err := s.e.Restic.Dump(ctx, s.snap.ID, filepath.Join(s.root, layout.DumpFile), w); err != nil {
			return fmt.Errorf("read dump.sql from the snapshot: %w", err)
		}
		return nil
	})
}

func (s *resticSource) filestore(ctx context.Context, staging string) (string, string, error) {
	err := s.e.Restic.Restore(ctx, s.snap.ID, staging,
		filepath.Join(s.root, layout.FilestoreDir), filepath.Join(s.root, layout.RepairedDir))
	if err != nil {
		return "", "", fmt.Errorf("restore the filestore: %w", err)
	}
	return filepath.Join(staging, s.root, layout.FilestoreDir), filepath.Join(staging, s.root, layout.RepairedDir), nil
}

// ---- Odoo backup files ------------------------------------------------------

// Odoo backup file kinds.
const (
	extZip  = ".zip"
	extDump = ".dump"
)

// IsBackupFile reports whether name is an Odoo backup file the restore reads.
func IsBackupFile(name string) bool {
	return strings.HasSuffix(name, extZip) || strings.HasSuffix(name, extDump)
}

// rcloneRun runs rclone and returns its stdout.
// defaultRclone is the rclone executable when none is given.
const defaultRclone = "rclone"

func rcloneRun(ctx context.Context, bin string, args ...string) ([]byte, error) {
	if bin == "" {
		bin = defaultRclone
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"--retries", "3", "--low-level-retries", "5"}, args...)...)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 30 * time.Second
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rclone %s: %w: %s", args[0], err, tail(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// hasPattern reports whether a path segment is a pattern.
func hasPattern(seg string) bool {
	return strings.ContainsAny(seg, "*?[")
}

// PickFile resolves a backup file path whose last segment may be a pattern
// to one file: the match whose name sorts last. target is the store's root
// as rclone names it, file the path below it.
func PickFile(ctx context.Context, rclone, target, file string) (string, error) {
	dir, pattern := path.Split(file)
	if _, err := path.Match(pattern, ""); err != nil {
		return "", fmt.Errorf("bad pattern %q: %w", pattern, err)
	}
	if !hasPattern(pattern) {
		return file, nil
	}
	out, err := rcloneRun(ctx, rclone, "lsf", "--files-only", joinTarget(target, dir))
	if err != nil {
		return "", fmt.Errorf("list %s: %w", dir, err)
	}
	var matches []string
	for _, name := range strings.Split(string(out), "\n") {
		if ok, _ := path.Match(pattern, name); ok && name != "" {
			matches = append(matches, name)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no file in %q matches %s", strings.TrimSuffix(dir, "/"), pattern)
	}
	sort.Strings(matches)
	return dir + matches[len(matches)-1], nil
}

// joinTarget joins an rclone target ("remote:bucket/path" or a local folder)
// and a path below it.
func joinTarget(target, p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return target
	}
	if strings.HasSuffix(target, ":") || strings.HasSuffix(target, "/") {
		return target + p
	}
	return target + "/" + p
}

// fileSource is an Odoo backup file, copied from the store to the data
// volume (the restore needs room there for the database's files anyway).
type fileSource struct {
	r     *restoreRun
	name  string
	local string
	zip   *zip.ReadCloser
	// prefix is the folder inside the zip that holds dump.sql: "" or one
	// folder ("backup/") for a zip someone zipped again.
	prefix string
	warn   []string
}

func openFileSource(ctx context.Context, r *restoreRun, staging string) (*fileSource, error) {
	o := r.o
	name, err := PickFile(ctx, o.Rclone, o.StoreTarget, o.File)
	if err != nil {
		return nil, err
	}
	s := &fileSource{r: r, name: name, local: filepath.Join(staging, "backup"+path.Ext(name))}
	r.e.logf("copying %s from the store", name)
	if _, err := rcloneRun(ctx, o.Rclone, "copyto", joinTarget(o.StoreTarget, name), s.local); err != nil {
		return nil, fmt.Errorf("copy %s from the store: %w", name, err)
	}
	r.j.File = name
	if strings.HasSuffix(name, extDump) {
		s.warn = append(s.warn, "the backup is a .dump file, which holds no filestore: the restored instance has no attachment files")
		return s, nil
	}
	zr, err := zip.OpenReader(s.local)
	if err != nil {
		return nil, fmt.Errorf("%s is not a readable zip: %w", name, err)
	}
	s.zip = zr
	if s.prefix, err = zipPrefix(&zr.Reader); err != nil {
		s.close()
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return s, nil
}

// zipPrefix finds the folder holding dump.sql: the top of the zip, or one
// folder down.
func zipPrefix(zr *zip.Reader) (string, error) {
	var found []string
	for _, f := range zr.File {
		dir, base := path.Split(f.Name)
		if base != layout.DumpFile || strings.Count(dir, "/") > 1 {
			continue
		}
		found = append(found, dir)
	}
	switch len(found) {
	case 0:
		return "", final(errors.New("the zip holds no dump.sql at its top or one folder down; it is not an Odoo backup"))
	case 1:
		return found[0], nil
	}
	return "", final(fmt.Errorf("the zip holds %d dump.sql files; an Odoo backup holds one", len(found)))
}

func (s *fileSource) describe() string   { return "file " + s.name }
func (s *fileSource) warnings() []string { return s.warn }

func (s *fileSource) close() {
	if s.zip != nil {
		_ = s.zip.Close()
		s.zip = nil
	}
}

func (s *fileSource) entry(name string) *zip.File {
	for _, f := range s.zip.File {
		if f.Name == s.prefix+name {
			return f
		}
	}
	return nil
}

func (s *fileSource) manifest(context.Context) (manifest.Manifest, error) {
	if s.zip == nil {
		return manifest.Manifest{}, nil
	}
	f := s.entry(layout.ManifestFile)
	if f == nil {
		s.warn = append(s.warn, "the zip has no manifest.json")
		return manifest.Manifest{}, nil
	}
	rc, err := f.Open()
	if err != nil {
		return manifest.Manifest{}, err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return manifest.Manifest{}, fmt.Errorf("read manifest.json: %w", err)
	}
	return manifest.Parse(data)
}

func (s *fileSource) loadDatabase(ctx context.Context, r *restoreRun, database string, m manifest.Manifest) error {
	if s.zip == nil {
		return r.loadArchive(ctx, database, s.local)
	}
	f := s.entry(layout.DumpFile)
	return r.loadSQL(ctx, database, m, func(_ context.Context, w io.Writer) error {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer func() { _ = rc.Close() }()
		if _, err := io.Copy(w, rc); err != nil {
			return fmt.Errorf("read dump.sql from the zip: %w", err)
		}
		return nil
	})
}

// filestore unpacks <prefix>filestore/ and <prefix>filestore-repaired/.
// Only regular files with plain relative names are written; anything else in
// those folders is refused, so a crafted zip cannot write outside staging.
func (s *fileSource) filestore(_ context.Context, staging string) (string, string, error) {
	files := filepath.Join(staging, "zip", layout.FilestoreDir)
	repaired := filepath.Join(staging, "zip", layout.RepairedDir)
	if s.zip == nil {
		return files, "", nil
	}
	count := 0
	for _, f := range s.zip.File {
		rel, ok := strings.CutPrefix(f.Name, s.prefix)
		if !ok {
			continue
		}
		var dst string
		switch {
		case strings.HasPrefix(rel, layout.FilestoreDir+"/"):
			dst = files
			rel = strings.TrimPrefix(rel, layout.FilestoreDir+"/")
		case strings.HasPrefix(rel, layout.RepairedDir+"/"):
			dst = repaired
			rel = strings.TrimPrefix(rel, layout.RepairedDir+"/")
		default:
			continue
		}
		if f.FileInfo().IsDir() || rel == "" {
			continue
		}
		if !f.Mode().IsRegular() {
			return "", "", final(fmt.Errorf("the zip's filestore holds %s, which is not a regular file", f.Name))
		}
		if !safeStoreName(rel) {
			return "", "", final(fmt.Errorf("the zip's filestore holds a file with an unsafe name: %q", f.Name))
		}
		if err := extract(f, filepath.Join(dst, filepath.FromSlash(rel))); err != nil {
			return "", "", err
		}
		count++
	}
	s.r.e.logf("unpacked %d filestore files", count)
	return files, repaired, nil
}

func extract(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		return fmt.Errorf("unpack %s: %w", f.Name, err)
	}
	return out.Close()
}

// loadArchive restores a pg_dump custom-format archive (Odoo's .dump) the way
// Odoo does, without its owner or privileges. The newest pg_restore turns it
// into SQL, since the pg_dump that wrote it (Odoo's images ship a recent one)
// can be newer than the server, and psql for the server loads that.
func (r *restoreRun) loadArchive(ctx context.Context, database, file string) error {
	binDir, err := r.e.Clients.Newest()
	if err != nil {
		return err
	}
	return r.loadSQL(ctx, database, manifest.Manifest{}, func(ctx context.Context, w io.Writer) error {
		cmd := exec.CommandContext(ctx, pg.Tool(binDir, "pg_restore"),
			"--no-owner", "--no-privileges", "--file", "-", file)
		cmd.Stdout = w
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		cmd.WaitDelay = 30 * time.Second
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("pg_restore: %w: %s", err, tail(stderr.String()))
		}
		return nil
	})
}
