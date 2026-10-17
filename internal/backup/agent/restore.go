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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/manifest"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/pg"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// Restore modes.
const (
	// ModeRestore loads a snapshot and swaps it in.
	ModeRestore = "restore"
	// ModeUndo swaps the data an earlier restore replaced back in.
	ModeUndo = "undo"
)

// Journal stages, in order.
const (
	stageNew       = ""
	stageRestored  = "restored"
	stageSwapping  = "swapping"
	stageSwapped   = "swapped"
	journalVersion = 1
)

// journal is the restore's record in the data PVC. A Job that is retried
// reads it to pick up where the previous attempt stopped; once the swap has
// started it is only ever finished.
type journal struct {
	Version int    `json:"version"`
	Mode    string `json:"mode"`
	Suffix  string `json:"suffix"`
	Stage   string `json:"stage"`
	// Source is the database and filestore folder swapped in.
	Source string `json:"source"`
	// Set when the swap starts: what existed then.
	SourceDB  bool `json:"sourceDB"`
	SourceFS  bool `json:"sourceFS"`
	HadLiveDB bool `json:"hadLiveDB"`
	HadLiveFS bool `json:"hadLiveFS"`
	// LiveComment is the live database's comment (the operator's owner
	// tag), carried over to the database swapped in.
	LiveComment *string `json:"liveComment,omitempty"`

	// From the backup.
	Image          string   `json:"image,omitempty"`
	UpgradeToken   string   `json:"upgradeToken,omitempty"`
	OdooVersion    string   `json:"odooVersion,omitempty"`
	MissingModules []string `json:"missingModules,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

func readJournal(path string) (*journal, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("restore journal %s is unreadable: %w", path, err)
	}
	return &j, nil
}

func (j *journal) save(path string) error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// RestoreOptions describe one restore or undo.
type RestoreOptions struct {
	Mode string
	// Suffix names this restore's databases and folders.
	Suffix string
	// Snapshot to restore (ModeRestore).
	Snapshot string
	// UndoSuffix is the suffix of the restore being undone (ModeUndo).
	UndoSuffix string
	// DataRoot is the root of the instance's data PVC (layout.DataMount).
	DataRoot string
	// ExpectModules are the modules the instance should have (spec.modules);
	// the ones the restored database lacks are reported.
	ExpectModules []string
	// CheckSample is how many restored files have their content checked
	// against their name.
	CheckSample int
}

// restoreRun is one restore attempt.
type restoreRun struct {
	e     *Env
	o     RestoreOptions
	maint *pgx.Conn
	live  string
	j     *journal
	path  string
}

func (r *restoreRun) fs(name string) string {
	return filepath.Join(r.o.DataRoot, layout.FilestorePath(name))
}

// Restore restores a snapshot (or undoes an earlier restore) in place. Odoo
// must be stopped. Until the swap starts, live data is untouched and a
// failure leaves it as it was; once it has started, Restore (run again by a
// retried Job) finishes it.
func Restore(ctx context.Context, e *Env, o RestoreOptions) result.Result {
	if !layout.ValidSuffix(o.Suffix) || (o.Mode == ModeUndo && !layout.ValidSuffix(o.UndoSuffix)) {
		return result.Failed(result.StepConnect, errors.New("invalid restore suffix"))
	}
	if o.Mode != ModeRestore && o.Mode != ModeUndo {
		return result.Failed(result.StepConnect, fmt.Errorf("unknown restore mode %q", o.Mode))
	}
	r := &restoreRun{e: e, o: o, live: e.Config.Name, path: filepath.Join(o.DataRoot, layout.JournalFile(o.Suffix))}
	j, err := readJournal(r.path)
	if err != nil {
		return result.Failed(result.StepConnect, err)
	}
	if j == nil {
		j = &journal{Version: journalVersion, Mode: o.Mode, Suffix: o.Suffix}
	}
	if j.Mode != o.Mode {
		return result.Failed(result.StepConnect, fmt.Errorf("restore journal is for a %s, not a %s", j.Mode, o.Mode))
	}
	r.j = j
	r.maint, err = pg.Connect(ctx, e.Config, e.MaintenanceDB)
	if err != nil {
		return r.failed(result.StepConnect, err)
	}
	defer func() { _ = r.maint.Close(context.Background()) }()

	if j.Stage == stageNew {
		var step string
		if o.Mode == ModeRestore {
			step, err = r.load(ctx)
		} else {
			step, err = r.prepareUndo(ctx)
		}
		if err != nil {
			if cleanupErr := r.discardSource(ctx); cleanupErr != nil {
				e.logf("could not remove the half-restored copy: %v", cleanupErr)
			}
			return r.failed(step, err)
		}
	}
	if err := r.swap(ctx); err != nil {
		return r.failed(result.StepSwap, err)
	}
	e.logf("restore %s done", o.Suffix)
	return result.Result{
		OK:             true,
		LiveChanged:    true,
		PreviousKept:   j.HadLiveDB || j.HadLiveFS,
		Image:          j.Image,
		UpgradeToken:   j.UpgradeToken,
		OdooVersion:    j.OdooVersion,
		MissingModules: j.MissingModules,
		Warnings:       j.Warnings,
	}
}

func (r *restoreRun) failed(step string, err error) result.Result {
	res := result.Failed(step, err)
	res.LiveChanged = r.j != nil && (r.j.Stage == stageSwapping || r.j.Stage == stageSwapped)
	return res
}

// load restores the snapshot into <db>_restore_<suffix> and checks it.
func (r *restoreRun) load(ctx context.Context) (string, error) {
	e, o := r.e, r.o
	source := layout.RestoreName(r.live, o.Suffix)
	r.j.Source = source

	snap, err := e.Restic.Snapshot(ctx, o.Snapshot)
	if err != nil {
		return result.StepFetch, err
	}
	if len(snap.Paths) != 1 {
		return result.StepFetch, fmt.Errorf("snapshot %s holds %d paths; an Odoo backup holds one", o.Snapshot, len(snap.Paths))
	}
	root := snap.Paths[0]
	var mbuf bytes.Buffer
	if err := e.Restic.Dump(ctx, snap.ID, filepath.Join(root, layout.ManifestFile), &mbuf); err != nil {
		return result.StepFetch, fmt.Errorf("read manifest.json: %w", err)
	}
	m, err := manifest.Parse(mbuf.Bytes())
	if err != nil {
		return result.StepFetch, err
	}

	liveInfo, err := pg.Database(ctx, r.maint, r.live)
	if err != nil {
		return result.StepConnect, err
	}
	if liveInfo.Exists {
		liveSeries, err := r.liveSeries(ctx)
		if err != nil {
			return result.StepConnect, err
		}
		if err := manifest.CheckSeries(m.Series(), liveSeries); err != nil {
			return result.StepCheck, err
		}
	}

	// Leftovers of an attempt that stopped before the swap.
	if err := r.discardSource(ctx); err != nil {
		return result.StepLoad, err
	}
	e.logf("loading snapshot %s into database %s", snap.ShortID, source)
	if err := pg.CreateLike(ctx, r.maint, source, liveInfo); err != nil {
		return result.StepLoad, err
	}
	if err := r.loadDump(ctx, snap.ID, filepath.Join(root, layout.DumpFile), source, m); err != nil {
		return result.StepLoad, err
	}
	if err := e.fault(FaultRestoreLoaded); err != nil {
		return result.StepLoad, err
	}
	if err := r.loadFilestore(ctx, snap.ID, root, source); err != nil {
		return result.StepLoad, err
	}
	if err := e.fault(FaultRestoreFilestore); err != nil {
		return result.StepLoad, err
	}
	warnings, missingModules, err := r.check(ctx, source, m)
	if err != nil {
		return result.StepCheck, err
	}
	if err := e.fault(FaultRestoreChecked); err != nil {
		return result.StepCheck, err
	}
	if m.Operator != nil {
		r.j.Image = m.Operator.Image
		r.j.UpgradeToken = m.Operator.UpgradeToken
	}
	r.j.OdooVersion = m.Series()
	r.j.MissingModules = missingModules
	r.j.Warnings = warnings
	r.j.Stage = stageRestored
	if err := r.j.save(r.path); err != nil {
		return result.StepLoad, err
	}
	return "", nil
}

// liveSeries is the Odoo series of the live database ("" for a database that
// is not an Odoo database yet).
func (r *restoreRun) liveSeries(ctx context.Context) (string, error) {
	conn, err := pg.Connect(ctx, r.e.Config, r.live)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	v, err := pg.BaseVersion(ctx, conn)
	if err != nil {
		return "", err
	}
	return manifest.Series(v), nil
}

// discardSource removes this restore's own copy (never live data, never
// anything once the swap has started).
func (r *restoreRun) discardSource(ctx context.Context) error {
	if r.o.Mode != ModeRestore || r.j.Stage == stageSwapping || r.j.Stage == stageSwapped {
		return nil
	}
	source := layout.RestoreName(r.live, r.o.Suffix)
	if err := layout.CheckName(source, r.live, r.o.Suffix); err != nil {
		return err
	}
	if err := pg.Drop(ctx, r.maint, source); err != nil {
		return err
	}
	if err := os.RemoveAll(r.fs(source)); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(r.o.DataRoot, layout.StagingDir(r.o.Suffix)))
}

// loadDump streams dump.sql from the snapshot into psql.
func (r *restoreRun) loadDump(ctx context.Context, snapshot, file, database string, m manifest.Manifest) error {
	e := r.e
	conn, err := pg.Connect(ctx, e.Config, database)
	if err != nil {
		return err
	}
	serverVersion, err := pg.ServerVersionNum(ctx, conn)
	_ = conn.Close(ctx)
	if err != nil {
		return err
	}
	binDir, err := e.Clients.Pick(pg.Major(serverVersion))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	h := sha256.New()
	counter := &countingWriter{}
	dumpErr := make(chan error, 1)
	go func() {
		err := e.Restic.Dump(ctx, snapshot, file, io.MultiWriter(pw, h, counter))
		_ = pw.CloseWithError(err)
		dumpErr <- err
	}()
	cmd := exec.CommandContext(ctx, pg.Tool(binDir, "psql"),
		"--no-psqlrc", "--quiet", "--set", "ON_ERROR_STOP=1", "--output", os.DevNull)
	cmd.Env = append(os.Environ(), e.Config.Env(database)...)
	cmd.Stdin = pr
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 30 * time.Second
	psqlErr := cmd.Run()
	if psqlErr != nil {
		cancel()
		_ = pr.CloseWithError(psqlErr)
	}
	derr := <-dumpErr
	if psqlErr != nil {
		return fmt.Errorf("psql: %w: %s", psqlErr, tail(stderr.String()))
	}
	if derr != nil {
		return fmt.Errorf("read dump.sql from the snapshot: %w", derr)
	}
	if m.Operator != nil && m.Operator.DumpSHA256 != "" {
		if got := hex.EncodeToString(h.Sum(nil)); got != m.Operator.DumpSHA256 {
			return fmt.Errorf("dump.sql has sha256 %s but the backup recorded %s", got, m.Operator.DumpSHA256)
		}
	}
	e.logf("loaded %d bytes of SQL", counter.n)
	return nil
}

// loadFilestore restores the filestore into <dataRoot>/filestore/<source>.
// Both live in the data PVC, so moving the files in is a rename.
func (r *restoreRun) loadFilestore(ctx context.Context, snapshot, root, source string) error {
	staging := filepath.Join(r.o.DataRoot, layout.StagingDir(r.o.Suffix))
	if err := os.MkdirAll(staging, 0o750); err != nil {
		return err
	}
	err := r.e.Restic.Restore(ctx, snapshot, staging,
		filepath.Join(root, layout.FilestoreDir), filepath.Join(root, layout.RepairedDir))
	if err != nil {
		return fmt.Errorf("restore the filestore: %w", err)
	}
	dst := r.fs(source)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	restored := filepath.Join(staging, root, layout.FilestoreDir)
	if ok, err := exists(restored); err != nil {
		return err
	} else if ok {
		if err := os.Rename(restored, dst); err != nil {
			return err
		}
	} else if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	repaired := filepath.Join(staging, root, layout.RepairedDir)
	err = filepath.WalkDir(repaired, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && path == repaired {
				return filepath.SkipDir
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(repaired, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.Rename(path, target)
	})
	if err != nil {
		return fmt.Errorf("put back the repaired attachments: %w", err)
	}
	if err := syncDir(filepath.Dir(dst)); err != nil {
		return err
	}
	return os.RemoveAll(staging)
}

// check compares the restored copy with the manifest: every table's row
// count, every attachment's file, and the content of a sample of files.
func (r *restoreRun) check(ctx context.Context, database string, m manifest.Manifest) ([]string, []string, error) {
	conn, err := pg.Connect(ctx, r.e.Config, database)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var warnings []string

	if m.Operator != nil && m.Operator.RowCounts != nil {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return nil, nil, err
		}
		counts, err := pg.RowCounts(ctx, tx)
		_ = tx.Rollback(ctx)
		if err != nil {
			return nil, nil, err
		}
		if diff := diffCounts(m.Operator.RowCounts, counts); len(diff) > 0 {
			return nil, nil, fmt.Errorf("the restored database does not match the backup: %s", strings.Join(limit(diff, 10), "; "))
		}
	} else {
		warnings = append(warnings, "the backup has no row counts to check the restored database against")
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	names, err := pg.StoreNames(ctx, tx)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, err
	}
	installed, err := pg.InstalledModules(ctx, tx)
	_ = tx.Rollback(ctx)
	if err != nil {
		return nil, nil, err
	}
	knownMissing := map[string]bool{}
	if m.Operator != nil {
		for _, n := range m.Operator.Files.Missing {
			knownMissing[n] = true
		}
	}
	dir := r.fs(database)
	var lost, expected []string
	present := make([]string, 0, len(names))
	for _, n := range names {
		switch {
		case !safeStoreName(n) || !isFile(filepath.Join(dir, n)):
			if knownMissing[n] {
				expected = append(expected, n)
			} else {
				lost = append(lost, n)
			}
		default:
			present = append(present, n)
		}
	}
	if len(lost) > 0 && m.Operator != nil {
		return nil, nil, fmt.Errorf("%d attachment(s) the backup holds are missing after the restore, e.g. %s",
			len(lost), strings.Join(limit(lost, 5), ", "))
	}
	if len(lost) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d attachment(s) are not in the backup, e.g. %s", len(lost), strings.Join(limit(lost, 5), ", ")))
	}
	if len(expected) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d attachment(s) were already missing when the backup was taken", len(expected)))
	}
	var mismatched []string
	for _, i := range sampleIndexes(len(present), r.o.CheckSample) {
		ok, err := contentMatchesName(filepath.Join(dir, present[i]))
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			mismatched = append(mismatched, present[i])
		}
	}
	if len(mismatched) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d sampled file(s) do not match their name (they were damaged before the backup), e.g. %s",
			len(mismatched), mismatched[0]))
	}
	r.e.logf("checked %d attachments (%d sampled)", len(names), len(sampleIndexes(len(present), r.o.CheckSample)))
	return warnings, missingModules(r.o.ExpectModules, installed), nil
}

// missingModules are the expected modules a database does not have installed.
func missingModules(expect []string, installed map[string]string) []string {
	var missing []string
	for _, mod := range expect {
		if _, ok := installed[mod]; !ok {
			missing = append(missing, mod)
		}
	}
	return missing
}

// diffCounts describes the tables whose row counts differ.
func diffCounts(want, got map[string]int64) []string {
	var diff []string
	for t, n := range want {
		g, ok := got[t]
		switch {
		case !ok:
			diff = append(diff, fmt.Sprintf("table %s is missing", t))
		case g != n:
			diff = append(diff, fmt.Sprintf("table %s has %d rows instead of %d", t, g, n))
		}
	}
	for t := range got {
		if _, ok := want[t]; !ok {
			diff = append(diff, fmt.Sprintf("table %s is not in the backup", t))
		}
	}
	sort.Strings(diff)
	return diff
}

// prepareUndo checks that the data being put back is still there.
func (r *restoreRun) prepareUndo(ctx context.Context) (string, error) {
	source := layout.PrevName(r.live, r.o.UndoSuffix)
	r.j.Source = source
	dbOK, err := pg.Exists(ctx, r.maint, source)
	if err != nil {
		return result.StepConnect, err
	}
	if !dbOK {
		return result.StepCheck, fmt.Errorf("database %s, which the undone restore replaced, no longer exists", source)
	}
	conn, err := pg.Connect(ctx, r.e.Config, source)
	if err != nil {
		return result.StepConnect, err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	v, err := pg.BaseVersion(ctx, conn)
	if err != nil {
		return result.StepCheck, err
	}
	r.j.OdooVersion = manifest.Series(v)
	if v != "" {
		installed, err := pg.InstalledModules(ctx, conn)
		if err != nil {
			return result.StepCheck, err
		}
		r.j.MissingModules = missingModules(r.o.ExpectModules, installed)
	}
	r.j.Stage = stageRestored
	if err := r.j.save(r.path); err != nil {
		return result.StepCheck, err
	}
	return "", nil
}

// swap puts the source in place of the live database and filestore, keeping
// the live ones as <db>_prev_<suffix>. The database renames run in one
// transaction; the two folder renames cannot, so the journal records that
// the swap started and a retry finishes whatever is left.
func (r *restoreRun) swap(ctx context.Context) error {
	j := r.j
	prev := layout.PrevName(r.live, r.o.Suffix)
	if j.Stage == stageRestored {
		liveInfo, err := pg.Database(ctx, r.maint, r.live)
		if err != nil {
			return err
		}
		srcDB, err := pg.Exists(ctx, r.maint, j.Source)
		if err != nil {
			return err
		}
		srcFS, err := exists(r.fs(j.Source))
		if err != nil {
			return err
		}
		liveFS, err := exists(r.fs(r.live))
		if err != nil {
			return err
		}
		if !srcDB {
			return fmt.Errorf("database %s to swap in does not exist", j.Source)
		}
		if prevDB, err := pg.Exists(ctx, r.maint, prev); err != nil {
			return err
		} else if prevDB {
			return fmt.Errorf("database %s already exists; refusing to overwrite it", prev)
		}
		if prevFS, err := exists(r.fs(prev)); err != nil {
			return err
		} else if prevFS {
			return fmt.Errorf("filestore folder %s already exists; refusing to overwrite it", prev)
		}
		j.SourceDB, j.SourceFS = srcDB, srcFS
		j.HadLiveDB, j.HadLiveFS = liveInfo.Exists, liveFS
		j.LiveComment = liveInfo.Comment
		j.Stage = stageSwapping
		if err := j.save(r.path); err != nil {
			return err
		}
		r.e.logf("swap started: live %s → %s, %s → live", r.live, prev, j.Source)
		if err := r.e.fault(FaultSwapStarted); err != nil {
			return err
		}
	}
	if j.Stage == stageSwapping {
		if err := r.swapDatabase(ctx, prev); err != nil {
			return err
		}
		if err := r.e.fault(FaultSwapDatabases); err != nil {
			return err
		}
		if err := r.swapFilestore(prev); err != nil {
			return err
		}
		if err := r.e.fault(FaultSwapFilestore); err != nil {
			return err
		}
		j.Stage = stageSwapped
		if err := j.save(r.path); err != nil {
			return err
		}
	}
	return nil
}

func (r *restoreRun) swapDatabase(ctx context.Context, prev string) error {
	j := r.j
	srcExists, err := pg.Exists(ctx, r.maint, j.Source)
	if err != nil {
		return err
	}
	if !srcExists {
		// The renames already committed.
		if ok, err := pg.Exists(ctx, r.maint, r.live); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("neither %s nor %s exists after the swap", j.Source, r.live)
		}
		return nil
	}
	liveExists, err := pg.Exists(ctx, r.maint, r.live)
	if err != nil {
		return err
	}
	var renames []pg.Rename
	if liveExists {
		renames = append(renames, pg.Rename{From: r.live, To: prev})
	}
	renames = append(renames, pg.Rename{From: j.Source, To: r.live})
	if err := pg.SwapNames(ctx, r.maint, renames, j.LiveComment); err != nil {
		return err
	}
	r.e.logf("databases swapped")
	return nil
}

func (r *restoreRun) swapFilestore(prev string) error {
	j := r.j
	src, live, old := r.fs(j.Source), r.fs(r.live), r.fs(prev)
	srcExists, err := exists(src)
	if err != nil {
		return err
	}
	liveExists, err := exists(live)
	if err != nil {
		return err
	}
	oldExists, err := exists(old)
	if err != nil {
		return err
	}
	switch {
	case j.SourceFS && srcExists:
		if liveExists {
			if oldExists {
				return fmt.Errorf("filestore folders %s, %s and %s all exist", j.Source, r.live, prev)
			}
			if err := os.Rename(live, old); err != nil {
				return err
			}
			if err := r.e.fault(FaultSwapLiveAside); err != nil {
				return err
			}
		}
		if err := os.Rename(src, live); err != nil {
			return err
		}
	case !j.SourceFS && liveExists && !oldExists:
		// The data swapped in has no filestore: the live folder only moves aside.
		if err := os.Rename(live, old); err != nil {
			return err
		}
	}
	if ok, err := exists(live); err != nil {
		return err
	} else if j.SourceFS && !ok {
		return fmt.Errorf("filestore folder %s is missing after the swap", r.live)
	}
	if err := syncDir(filepath.Dir(live)); err != nil {
		return err
	}
	r.e.logf("filestore folders swapped")
	return nil
}
