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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/manifest"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/pg"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// A restore drill runs in three containers of one pod, which share a
// scratch folder (an emptyDir, never the instance's data volume):
//
//  1. DrillLoad restores the snapshot into the scratch database
//     <db>_drill_<suffix> and the scratch folder, checks it as a restore
//     would, and writes its outcome to DrillStateFile.
//  2. The instance's Odoo image loads the registry of the scratch database
//     (--stop-after-init) when the load succeeded, and writes its exit code
//     to DrillOdooExitFile and its log to DrillOdooLogFile.
//  3. DrillFinish drops the scratch database and reports the whole drill.
//
// Each step exits 0 on a failed drill, so the next still runs and the
// scratch database is always dropped.
const (
	DrillStateFile    = "drill.json"
	DrillLoadedFile   = "loaded"
	DrillOdooExitFile = "odoo.exit"
	DrillOdooLogFile  = "odoo.log"
)

// DrillOptions describe one restore drill.
type DrillOptions struct {
	// Suffix names the scratch database.
	Suffix string
	// Snapshot to restore.
	Snapshot string
	// Root is the scratch folder; the filestore goes to Root/filestore/<db>.
	Root string
	// ExpectModules and CheckSample are as for a restore.
	ExpectModules []string
	CheckSample   int
}

// DrillLoad restores the snapshot into scratch space and checks it. It drops
// the scratch databases earlier drills of the instance left behind first.
// Its outcome goes to DrillStateFile; the error it returns is only that of
// writing it.
func DrillLoad(ctx context.Context, e *Env, o DrillOptions) error {
	res := drillLoad(ctx, e, o)
	data, err := json.Marshal(res)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(o.Root, DrillStateFile), data); err != nil {
		return err
	}
	if !res.OK {
		e.logf("drill load failed at step %s: %s", res.Step, res.Error)
		return nil
	}
	return os.WriteFile(filepath.Join(o.Root, DrillLoadedFile), nil, 0o644)
}

func drillLoad(ctx context.Context, e *Env, o DrillOptions) result.Result {
	if !layout.ValidSuffix(o.Suffix) {
		return result.Failed(result.StepConnect, errors.New("invalid drill suffix"))
	}
	live := e.Config.Name
	scratch := layout.DrillName(live, o.Suffix)
	r := &restoreRun{e: e, live: live, o: RestoreOptions{
		Mode: ModeRestore, Suffix: o.Suffix, Snapshot: o.Snapshot, DataRoot: o.Root,
		ExpectModules: o.ExpectModules, CheckSample: o.CheckSample,
	}}
	var err error
	r.maint, err = pg.Connect(ctx, e.Config, e.MaintenanceDB)
	if err != nil {
		return result.Failed(result.StepConnect, err)
	}
	defer func() { _ = r.maint.Close(context.Background()) }()
	if err := dropDrillDatabases(ctx, e, r.maint, ""); err != nil {
		return result.Failed(result.StepConnect, err)
	}

	snap, err := e.Restic.Snapshot(ctx, o.Snapshot)
	if err != nil {
		return result.Failed(result.StepFetch, err)
	}
	if len(snap.Paths) != 1 {
		return result.Failed(result.StepFetch, fmt.Errorf("snapshot %s holds %d paths; an Odoo backup holds one", o.Snapshot, len(snap.Paths)))
	}
	root := snap.Paths[0]
	var mbuf bytes.Buffer
	if err := e.Restic.Dump(ctx, snap.ID, filepath.Join(root, layout.ManifestFile), &mbuf); err != nil {
		return result.Failed(result.StepFetch, fmt.Errorf("read manifest.json: %w", err))
	}
	m, err := manifest.Parse(mbuf.Bytes())
	if err != nil {
		return result.Failed(result.StepFetch, err)
	}
	liveInfo, err := pg.Database(ctx, r.maint, live)
	if err != nil {
		return result.Failed(result.StepConnect, err)
	}
	e.logf("loading snapshot %s into scratch database %s", snap.ShortID, scratch)
	if err := pg.CreateLike(ctx, r.maint, scratch, liveInfo); err != nil {
		return result.Failed(result.StepLoad, err)
	}
	if err := r.loadDump(ctx, snap.ID, filepath.Join(root, layout.DumpFile), scratch, m); err != nil {
		return result.Failed(result.StepLoad, err)
	}
	if err := r.loadFilestore(ctx, snap.ID, root, scratch); err != nil {
		return result.Failed(result.StepLoad, err)
	}
	warnings, missing, err := r.check(ctx, scratch, m)
	if err != nil {
		return result.Failed(result.StepCheck, err)
	}
	e.logf("snapshot %s restored and checked", snap.ShortID)
	return result.Result{OK: true, OdooVersion: m.Series(), Warnings: warnings, MissingModules: missing}
}

// dropDrillDatabases drops the instance's scratch drill databases (only the
// one of suffix when it is set).
func dropDrillDatabases(ctx context.Context, e *Env, maint *pgx.Conn, suffix string) error {
	live := e.Config.Name
	names, err := pg.Databases(ctx, maint)
	if err != nil {
		return err
	}
	for _, name := range names {
		s := layout.DrillSuffix(name, live)
		if s == "" || (suffix != "" && s != suffix) {
			continue
		}
		if err := layout.CheckName(name, live, s); err != nil {
			return err
		}
		if err := pg.Drop(ctx, maint, name); err != nil {
			return err
		}
		e.logf("dropped scratch database %s", name)
	}
	return nil
}

// DrillFinish drops the scratch database and reports the drill: the load's
// outcome, then Odoo's.
func DrillFinish(ctx context.Context, e *Env, o DrillOptions) result.Result {
	res := result.Result{}
	data, err := os.ReadFile(filepath.Join(o.Root, DrillStateFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
		res = result.Failed(result.StepLoad, errors.New("the load step ended without reporting"))
	case err != nil:
		res = result.Failed(result.StepLoad, err)
	default:
		if err := json.Unmarshal(data, &res); err != nil {
			res = result.Failed(result.StepLoad, fmt.Errorf("read the load step's report: %w", err))
		}
	}
	if res.OK {
		if err := odooOutcome(o.Root); err != nil {
			res = result.Failed(result.StepOdoo, err)
		}
	}

	maint, err := pg.Connect(ctx, e.Config, e.MaintenanceDB)
	if err == nil {
		err = dropDrillDatabases(ctx, e, maint, o.Suffix)
		_ = maint.Close(context.Background())
	}
	if err != nil {
		if res.OK {
			return result.Failed(result.StepCleanup, fmt.Errorf("the drill passed but its scratch database could not be dropped: %w", err))
		}
		res.Warnings = append(res.Warnings, "the scratch database could not be dropped: "+err.Error())
	}
	return res
}

// odooOutcome reads how Odoo's registry load ended.
func odooOutcome(root string) error {
	code, err := os.ReadFile(filepath.Join(root, DrillOdooExitFile))
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("the Odoo container did not run, or ended without an exit code")
	}
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(code)))
	if err != nil {
		return fmt.Errorf("odoo exit code %q: %w", strings.TrimSpace(string(code)), err)
	}
	if n == 0 {
		return nil
	}
	log, _ := os.ReadFile(filepath.Join(root, DrillOdooLogFile))
	return fmt.Errorf("the Odoo container could not load the restored database (exit code %d): %s", n, odooErrors(string(log)))
}

// odooErrors picks the lines of an Odoo log that say what went wrong: its
// ERROR and CRITICAL lines, or else its last lines.
func odooErrors(log string) string {
	lines := strings.Split(strings.TrimSpace(log), "\n")
	var picked []string
	for _, l := range lines {
		if strings.Contains(l, " ERROR ") || strings.Contains(l, " CRITICAL ") {
			picked = append(picked, strings.TrimSpace(l))
		}
	}
	if len(picked) == 0 {
		if len(lines) > 5 {
			lines = lines[len(lines)-5:]
		}
		picked = lines
	}
	return tail(strings.Join(limit(picked, 5), "\n"))
}
