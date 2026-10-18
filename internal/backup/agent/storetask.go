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
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/restic"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// Store tasks.
const (
	TaskCheck = "check"
	TaskPrune = "prune"
)

// Repository is one instance's repository in a store.
type Repository struct {
	// Name is the instance's folder, <namespace>/<deployment>.
	Name string
	// URL is the repository as restic names it.
	URL string
}

// StoreTaskOptions describe a check or prune of a store.
type StoreTaskOptions struct {
	Task         string
	Repositories []Repository
	// ReadData is the part of the data a check reads back ("3/7", "all"),
	// or "" for the structure only.
	ReadData string
}

// StoreTask checks or prunes each repository in turn, so one damaged or
// unreachable repository does not stop the others. A repository that does
// not exist yet (an instance never backed up) is skipped. Locks held by a
// running backup are waited for (restic --retry-lock).
func StoreTask(ctx context.Context, base *restic.Restic, logger *log.Logger, o StoreTaskOptions) result.Result {
	res := result.Result{OK: true}
	for _, repo := range o.Repositories {
		r := *base
		r.Env = append(append([]string(nil), base.Env...), "RESTIC_REPOSITORY="+repo.URL)
		size, found, err := storeTaskOne(ctx, &r, o)
		switch {
		case err != nil:
			msg := tail(err.Error())
			if errors.Is(err, restic.ErrLocked) {
				msg = "still locked by another restic process after waiting: " + msg
			}
			logger.Printf("%s %s failed: %s", o.Task, repo.Name, msg)
			res.OK = false
			res.RepositoryErrors = append(res.RepositoryErrors, repo.Name+": "+msg)
		case !found:
			logger.Printf("%s %s: no repository yet, skipped", o.Task, repo.Name)
			continue
		default:
			logger.Printf("%s %s: done, %d bytes stored", o.Task, repo.Name, size)
		}
		res.Repositories++
		res.StoredBytes += size
	}
	if !res.OK {
		res.Step = result.StepStoreTask
		res.Error = fmt.Sprintf("%d of %d repositories failed: %s", len(res.RepositoryErrors), res.Repositories,
			strings.Join(res.RepositoryErrors, "; "))
	}
	return res
}

func storeTaskOne(ctx context.Context, r *restic.Restic, o StoreTaskOptions) (int64, bool, error) {
	exists, err := r.Exists(ctx)
	if err != nil || !exists {
		return 0, false, err
	}
	if err := r.Unlock(ctx); err != nil {
		return 0, true, err
	}
	switch o.Task {
	case TaskCheck:
		err = r.Check(ctx, o.ReadData)
	case TaskPrune:
		err = r.Prune(ctx)
	default:
		err = fmt.Errorf("unknown task %q", o.Task)
	}
	if err != nil {
		return 0, true, err
	}
	size, err := r.RawDataSize(ctx)
	return size, true, err
}
