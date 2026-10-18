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
	"archive/tar"
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
	"strings"
	"time"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/restic"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// zipTimeFormat is the time in a zip's name, as Odoo's database manager
// names its backups.
const zipTimeFormat = "2006-01-02_15-04-05"

// ZipName is the name of the Odoo zip of a backup of database db whose
// snapshot was taken at t: <db>_<YYYY-MM-DD_HH-MM-SS>.zip, in UTC.
func ZipName(db string, t time.Time) string {
	return db + "_" + t.UTC().Format(zipTimeFormat) + extZip
}

// writeOdooZip writes snapshot snap as the zip Odoo's database manager
// makes: dump.sql, manifest.json and filestore/. The attachments a backup
// took from the previous one (filestore-repaired/) go into filestore/ too,
// so Odoo's own restore finds them. It returns how many filestore files the
// zip holds.
func writeOdooZip(ctx context.Context, rs *restic.Restic, snap restic.Snapshot, w io.Writer) (int, error) {
	if len(snap.Paths) != 1 {
		return 0, fmt.Errorf("snapshot %s holds %d paths; an Odoo backup holds one", snap.ShortID, len(snap.Paths))
	}
	root := strings.Trim(snap.Paths[0], "/") + "/"
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	dumpErr := make(chan error, 1)
	go func() {
		err := rs.DumpTar(ctx, snap.ID, snap.Paths[0], pw)
		_ = pw.CloseWithError(err)
		dumpErr <- err
	}()
	files, err := tarToOdooZip(tar.NewReader(pr), root, w)
	if err != nil {
		cancel()
		_ = pr.CloseWithError(err)
		<-dumpErr
		return 0, err
	}
	if err := <-dumpErr; err != nil {
		return 0, err
	}
	return files, nil
}

// tarToOdooZip writes the regular files of tr below root into an Odoo zip.
func tarToOdooZip(tr *tar.Reader, root string, w io.Writer) (int, error) {
	zw := zip.NewWriter(w)
	seen := map[string]bool{}
	files := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("read the snapshot: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name, ok := strings.CutPrefix(strings.TrimPrefix(hdr.Name, "/"), root)
		if !ok {
			return 0, fmt.Errorf("the snapshot holds %s, outside %s", hdr.Name, root)
		}
		if rest, ok := strings.CutPrefix(name, layout.RepairedDir+"/"); ok {
			name = layout.FilestoreDir + "/" + rest
		}
		isFile := strings.HasPrefix(name, layout.FilestoreDir+"/")
		if (!isFile && name != layout.DumpFile && name != layout.ManifestFile) || seen[name] {
			continue
		}
		seen[name] = true
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: hdr.ModTime})
		if err != nil {
			return 0, err
		}
		if _, err := io.Copy(fw, tr); err != nil {
			return 0, fmt.Errorf("write %s: %w", name, err)
		}
		if isFile {
			files++
		}
	}
	for _, need := range []string{layout.DumpFile, layout.ManifestFile} {
		if !seen[need] {
			return 0, fmt.Errorf("the snapshot holds no %s", need)
		}
	}
	return files, zw.Close()
}

// uploadZip writes a zip with write and uploads it to target (an rclone
// path) under a temporary name, then renames it, so the store never shows
// a zip that is cut short.
func uploadZip(ctx context.Context, rclone, target string, write func(io.Writer) (int, error)) (int, error) {
	dir, name := path.Split(target)
	if dir == "" {
		// A local file without a folder, or "remote:name".
		if i := strings.LastIndex(target, ":"); i >= 0 {
			dir, name = target[:i+1], target[i+1:]
		}
	}
	partial := dir + "." + name + ".partial"
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	writeErr := make(chan error, 1)
	var files int
	go func() {
		n, err := write(pw)
		files = n
		_ = pw.CloseWithError(err)
		writeErr <- err
	}()
	if rclone == "" {
		rclone = defaultRclone
	}
	cmd := exec.CommandContext(ctx, rclone, "--retries", "1", "--low-level-retries", "5", "rcat", partial)
	cmd.Env = os.Environ()
	cmd.Stdin = pr
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 30 * time.Second
	rcatErr := cmd.Run()
	if rcatErr != nil {
		cancel()
		_ = pr.CloseWithError(rcatErr)
	}
	werr := <-writeErr
	switch {
	case werr != nil:
		_, _ = rcloneRun(context.WithoutCancel(ctx), rclone, "deletefile", partial)
		return 0, werr
	case rcatErr != nil:
		_, _ = rcloneRun(context.WithoutCancel(ctx), rclone, "deletefile", partial)
		return 0, fmt.Errorf("upload %s: %w: %s", target, rcatErr, tail(stderr.String()))
	}
	if _, err := rcloneRun(ctx, rclone, "moveto", partial, target); err != nil {
		return 0, err
	}
	return files, nil
}

// ExportOptions describe one export of a snapshot as an Odoo zip.
type ExportOptions struct {
	// Snapshot is an ID, or "latest" for the newest with Host and Tags.
	Snapshot string
	Host     string
	Tags     []string
	// Output is a local file to write, "-" for stdout; or Target, an rclone
	// path to upload to (ending in / for a folder: the zip is named there
	// as zip copies are).
	Output string
	Target string
	// Rclone is the rclone executable; "rclone" when empty.
	Rclone string
	// Stdout is where Output "-" writes.
	Stdout io.Writer
}

// Export writes a snapshot as the zip Odoo's database manager makes, which
// Odoo restores like its own backups.
func Export(ctx context.Context, e *Env, o ExportOptions) result.Result {
	src, err := openResticSource(ctx, e, o.Snapshot, o.Host, o.Tags)
	if err != nil {
		return result.Failed(result.StepFetch, err)
	}
	snap := src.snap
	db := e.Config.Name
	if m, err := src.manifest(ctx); err == nil && m.DBName != "" {
		db = m.DBName
	}
	write := func(w io.Writer) (int, error) { return writeOdooZip(ctx, e.Restic, snap, w) }
	res := result.Result{OK: true, SnapshotID: snap.ID}
	var files int
	switch {
	case o.Target != "":
		target := o.Target
		if strings.HasSuffix(target, "/") || strings.HasSuffix(target, ":") {
			target += ZipName(db, snap.Time)
		}
		files, err = uploadZip(ctx, o.Rclone, target, write)
		res.File = target
	case o.Output == "-":
		files, err = write(o.Stdout)
		res.File = "-"
	default:
		files, err = writeLocalZip(o.Output, write)
		res.File = o.Output
	}
	if err != nil {
		return result.Failed(result.StepUpload, err)
	}
	e.logf("exported snapshot %s to %s with %d filestore file(s)", snap.ShortID, res.File, files)
	return res
}

// writeLocalZip writes the zip to file through a temporary file beside it.
func writeLocalZip(file string, write func(io.Writer) (int, error)) (int, error) {
	tmp, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+".*.partial")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	files, err := write(tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// CreateTemp makes the file private; an export is an ordinary file.
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err != nil {
		return 0, err
	}
	return files, os.Rename(tmp.Name(), file)
}
