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
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/pg"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// Cleanup deletes what a restore left behind: the data it replaced
// (<db>_prev_<suffix>), any half-restored copy (<db>_restore_<suffix>) and
// its journal. It refuses while the restore's swap is unfinished, and it
// only ever deletes those two names.
func Cleanup(ctx context.Context, e *Env, dataRoot, suffix string) result.Result {
	if !layout.ValidSuffix(suffix) {
		return result.Failed(result.StepCleanup, fmt.Errorf("invalid restore suffix %q", suffix))
	}
	live := e.Config.Name
	journalPath := filepath.Join(dataRoot, layout.JournalFile(suffix))
	j, err := readJournal(journalPath)
	if err != nil {
		return result.Failed(result.StepCleanup, err)
	}
	if j != nil && j.Stage == stageSwapping {
		return result.Failed(result.StepCleanup, fmt.Errorf("restore %s has not finished its swap; nothing is deleted", suffix))
	}
	maint, err := pg.Connect(ctx, e.Config, e.MaintenanceDB)
	if err != nil {
		return result.Failed(result.StepConnect, err)
	}
	defer func() { _ = maint.Close(context.Background()) }()

	for _, name := range []string{layout.PrevName(live, suffix), layout.RestoreName(live, suffix)} {
		if err := layout.CheckName(name, live, suffix); err != nil {
			return result.Failed(result.StepCleanup, err)
		}
		if name == live {
			return result.Failed(result.StepCleanup, fmt.Errorf("refusing to delete the live database %q", name))
		}
		if err := pg.Drop(ctx, maint, name); err != nil {
			return result.Failed(result.StepCleanup, err)
		}
		if err := os.RemoveAll(filepath.Join(dataRoot, layout.FilestorePath(name))); err != nil {
			return result.Failed(result.StepCleanup, err)
		}
		e.logf("deleted database and filestore folder %s", name)
	}
	if err := os.RemoveAll(filepath.Join(dataRoot, layout.StagingDir(suffix))); err != nil {
		return result.Failed(result.StepCleanup, err)
	}
	if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		return result.Failed(result.StepCleanup, err)
	}
	return result.Result{OK: true}
}

// Prepare makes sure the instance's filestore folder exists before a backup
// Job mounts it (an absent folder would otherwise be created by the kubelet,
// owned by root). It never changes an existing folder.
func Prepare(dataRoot, database string) error {
	dir := filepath.Join(dataRoot, layout.FilestorePath(database))
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
