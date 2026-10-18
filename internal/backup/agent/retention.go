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
	"strings"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/restic"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/retention"
)

// Retention is the retention a backup applies once its snapshot is safe.
type Retention struct {
	Policy retention.Policy
	// ProtectBackups are OdooBackups marked keep.
	ProtectBackups []string
	// ProtectSnapshots are snapshot IDs (or prefixes) a pending restore needs.
	ProtectSnapshots []string
}

// applyRetention forgets the instance's snapshots in r's repository that the
// policy no longer keeps, never current (the snapshot just written), and
// returns their IDs. Snapshots are only forgotten: the space comes back
// when the store is pruned.
func applyRetention(ctx context.Context, e *Env, rs *restic.Restic, host, current string, ret *Retention) ([]string, error) {
	snaps, err := rs.Snapshots(ctx, host, layout.TagDeployment+host)
	if err != nil {
		return nil, err
	}
	in := make([]retention.Snapshot, 0, len(snaps))
	for _, s := range snaps {
		in = append(in, retention.Snapshot{
			ID: s.ID, Time: s.Time,
			Kind:   s.TagValue(layout.TagKind),
			Backup: s.TagValue(layout.TagBackup),
		})
	}
	protect := retention.Protect{Backups: map[string]bool{}, SnapshotIDs: ret.ProtectSnapshots, Current: current}
	for _, b := range ret.ProtectBackups {
		protect.Backups[b] = true
	}
	forget, decisions, err := retention.Apply(ret.Policy, in, protect)
	if err != nil {
		return nil, err
	}
	kept := 0
	for _, d := range decisions {
		if d.Keep {
			kept++
		}
	}
	if len(forget) == 0 {
		e.logf("retention: keeping all %d snapshot(s)", kept)
		return nil, nil
	}
	ids := make([]string, 0, len(forget))
	names := make([]string, 0, len(forget))
	for _, f := range forget {
		if f.ID == current {
			return nil, fmt.Errorf("retention chose the snapshot just written (%s); nothing was forgotten", current)
		}
		ids = append(ids, f.ID)
		names = append(names, f.Backup)
	}
	e.logf("retention: keeping %d snapshot(s), forgetting %d: %s", kept, len(ids), strings.Join(names, ", "))
	if err := rs.Forget(ctx, ids...); err != nil {
		return nil, err
	}
	return ids, nil
}
