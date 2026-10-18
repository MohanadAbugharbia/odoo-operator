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

package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/jobspec"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/controller/reconcileloops"
	"github.com/MohanadAbugharbia/odoo-operator/internal/schedule"
	"github.com/MohanadAbugharbia/odoo-operator/pkg/utils"
)

// ConditionStoreSchedules is False when a check or prune schedule cannot be read.
const ConditionStoreSchedules = "Schedules"

// Reasons of store checks and prunes.
const (
	ReasonStoreCheckFailed = "StoreCheckFailed"
	ReasonStorePruneFailed = "StorePruneFailed"
	ReasonStoreTaskPassed  = "StoreTaskPassed"
)

// storeTask is one scheduled task of a store.
type storeTask struct {
	kind  string // jobspec.KindCheck or KindPrune
	expr  string
	last  **metav1.Time
	next  **metav1.Time
	sched *schedule.Schedule
}

// reconcileTasks runs the store's check and prune on their schedules, one
// Job at a time, and returns how long until the next is due. A time that
// comes while the other task runs waits for it; a missed time runs once.
//
//nolint:gocyclo // two schedules, one Job
func (r *OdooBackupStoreReconciler) reconcileTasks(ctx context.Context, store *odoov1.OdooBackupStore) (time.Duration, error) {
	st := &store.Status
	if st.TaskJob != "" {
		wait, err := r.observeTask(ctx, store)
		if err != nil || wait > 0 {
			return wait, err
		}
	}
	tz := zoneOrUTC(store.Spec.TimeZone)
	key := strings.Join([]string{store.Spec.Check.Schedule, store.Spec.Prune.Schedule, tz}, "|")
	changed := key != st.ScheduleKey
	st.ScheduleKey = key

	// Prune first when both are due: the check then reads what prune left.
	tasks := []*storeTask{
		{kind: jobspec.KindPrune, expr: store.Spec.Prune.Schedule, last: &st.LastPruneScheduleTime, next: &st.NextPruneTime},
		{kind: jobspec.KindCheck, expr: store.Spec.Check.Schedule, last: &st.LastCheckScheduleTime, next: &st.NextCheckTime},
	}
	var problems []string
	now := r.now()
	var wait time.Duration
	var due *storeTask
	var dueAt time.Time
	for _, t := range tasks {
		if strings.TrimSpace(t.expr) == "" {
			*t.last, *t.next = nil, nil
			continue
		}
		sched, err := schedule.Parse(t.expr, tz)
		if err != nil {
			problems = append(problems, fmt.Sprintf("spec.%s.schedule: %v", strings.TrimPrefix(t.kind, "store-"), err))
			*t.next = nil
			continue
		}
		t.sched = sched
		if changed || *t.last == nil {
			n := metav1.NewTime(now)
			*t.last = &n
		}
		last := (*t.last).Time
		at, next := sched.Step(&last, now)
		if !next.IsZero() {
			n := metav1.NewTime(next)
			*t.next = &n
			wait = minPositive(wait, next.Sub(now)+scheduleSlack)
		}
		if !at.IsZero() && due == nil {
			due, dueAt = t, at
		} else if !at.IsZero() {
			wait = minPositive(wait, time.Minute) // after the task that goes first
		}
	}
	if len(problems) > 0 {
		msg := strings.Join(problems, "; ")
		if c := meta.FindStatusCondition(st.Conditions, ConditionStoreSchedules); (c == nil || c.Message != msg) && r.Recorder != nil {
			r.Recorder.Event(store, corev1.EventTypeWarning, ReasonInvalidSchedule, msg)
		}
		utils.SetCondition(&st.Conditions, ConditionStoreSchedules, metav1.ConditionFalse, ReasonInvalidSchedule, msg, store.Generation)
	} else {
		meta.RemoveStatusCondition(&st.Conditions, ConditionStoreSchedules)
	}
	if due == nil {
		return wait, nil
	}

	repos, err := r.repositories(ctx, store)
	if err != nil {
		return 0, err
	}
	handled := metav1.NewTime(dueAt)
	if len(repos) == 0 {
		*due.last = &handled
		r.recordTask(store, due.kind, &odoov1.StoreTaskStatus{
			Time: &handled, Result: odoov1.StoreTaskPassed, Message: "no instance backs up to this store",
		})
		return minPositive(wait, time.Second), nil
	}
	image, err := r.Image(ctx)
	if err != nil {
		return time.Minute, nil //nolint:nilerr // retried; the probe reports the image problem
	}
	task := jobspec.StoreTask{Kind: due.kind, Scheduled: dueAt.UTC().Format(time.RFC3339), Repositories: repos}
	if due.kind == jobspec.KindCheck {
		task.Part = checkPart(store, st.CheckRuns)
	}
	job, err := jobspec.StoreTaskJob(image, store, task)
	if err != nil {
		return 0, err
	}
	if job.Annotations == nil {
		job.Annotations = map[string]string{}
	}
	job.Annotations[annotationRepositories] = strings.Join(repos, ",")
	if task.Part != "" {
		job.Annotations[annotationCheckPart] = task.Part
	}
	if err := controllerutil.SetControllerReference(store, &job, r.Scheme); err != nil {
		return 0, err
	}
	if err := r.deleteOldTaskJobs(ctx, store, due.kind); err != nil {
		return 0, err
	}
	if err := r.Create(ctx, &job); err != nil && !apierrors.IsAlreadyExists(err) {
		return 0, fmt.Errorf("create %s job: %w", due.kind, err)
	}
	*due.last = &handled
	st.TaskJob, st.TaskKind = job.Name, due.kind
	if due.kind == jobspec.KindCheck {
		st.CheckRuns++
	}
	log.FromContext(ctx).Info("Started store task", "store", store.Name, "task", due.kind, "job", job.Name, "repositories", len(repos))
	return 15 * time.Second, nil
}

// checkPart is the part of the data the n-th check (from 0) reads.
func checkPart(store *odoov1.OdooBackupStore, n int32) string {
	parts := int32(7)
	if p := store.Spec.Check.ReadDataParts; p != nil {
		parts = *p
	}
	switch parts {
	case 0:
		return ""
	case 1:
		return "all"
	}
	return fmt.Sprintf("%d/%d", n%parts+1, parts)
}

// repositories are the repositories of the instances in the store's
// namespace that back up or copy to it.
func (r *OdooBackupStoreReconciler) repositories(ctx context.Context, store *odoov1.OdooBackupStore) ([]string, error) {
	ods := &odoov1.OdooDeploymentList{}
	if err := r.reader().List(ctx, ods, client.InNamespace(store.Namespace)); err != nil {
		return nil, err
	}
	var repos []string
	for i := range ods.Items {
		for _, s := range ods.Items[i].Spec.Backup.Stores {
			if s == store.Name {
				repos = append(repos, layout.Repository(store.Namespace, ods.Items[i].Name))
				break
			}
		}
	}
	sort.Strings(repos)
	return repos, nil
}

// observeTask follows the running check or prune; a positive wait means it
// still runs.
func (r *OdooBackupStoreReconciler) observeTask(ctx context.Context, store *odoov1.OdooBackupStore) (time.Duration, error) {
	st := &store.Status
	od := &odoov1.OdooDeployment{}
	od.Namespace = store.Namespace
	od.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{Name: st.TaskJob, Namespace: store.Namespace}
	obs, err := reconcileloops.ObserveMaintenanceJob(ctx, r.Client, r.reader(), od)
	if err != nil {
		return 0, err
	}
	switch obs.State {
	case reconcileloops.JobStateMissing:
		// Deleted by hand: its scheduled time counts as handled.
		st.TaskJob, st.TaskKind = "", ""
		return 0, nil
	case reconcileloops.JobStateActive:
		return 30 * time.Second, nil
	}
	out, err := (&OdooDeploymentReconciler{Client: r.Client, APIReader: r.APIReader}).observeJob(ctx, store.Namespace, obs.Job.Name)
	if err != nil {
		return 0, err
	}
	now := metav1.NewTime(r.now())
	res := out.result
	outcome := &odoov1.StoreTaskStatus{Time: &now, Job: obs.Job.Name}
	verb := "checked"
	if st.TaskKind == jobspec.KindPrune {
		verb = "pruned"
	}
	if obs.State == reconcileloops.JobStateSucceeded && out.decoded && res.OK {
		outcome.Result = odoov1.StoreTaskPassed
		outcome.Message = fmt.Sprintf("%s %d repositories, %s stored", verb, res.Repositories, bytesText(res.StoredBytes))
		outcome.Job = ""
		st.Repositories = int32(res.Repositories)
		st.StoredBytes = res.StoredBytes
		if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, obs.Job); err != nil {
			return 0, err
		}
		if r.Recorder != nil {
			r.Recorder.Event(store, corev1.EventTypeNormal, ReasonStoreTaskPassed, outcome.Message)
		}
	} else {
		outcome.Result = odoov1.StoreTaskFailed
		outcome.Message = fmt.Sprintf("job %s %s; inspect with: kubectl -n %s logs job/%s",
			obs.Job.Name, failureText(out), store.Namespace, obs.Job.Name)
		reason := ReasonStoreCheckFailed
		if st.TaskKind == jobspec.KindPrune {
			reason = ReasonStorePruneFailed
		}
		if r.Recorder != nil {
			r.Recorder.Event(store, corev1.EventTypeWarning, reason, outcome.Message)
		}
	}
	outcome.Part = obs.Job.Annotations[annotationCheckPart]
	if repos := obs.Job.Annotations[annotationRepositories]; repos != "" {
		outcome.Repositories = strings.Split(repos, ",")
	}
	r.recordTask(store, st.TaskKind, outcome)
	st.TaskJob, st.TaskKind = "", ""
	return 0, nil
}

// Annotations a check or prune Job carries for the outcome it reports:
// the part of the data a check reads, and the repositories it goes through.
const (
	annotationCheckPart    = "odoo.abugharbia.com/check-part"
	annotationRepositories = "odoo.abugharbia.com/repositories"
)

func (r *OdooBackupStoreReconciler) recordTask(store *odoov1.OdooBackupStore, kind string, outcome *odoov1.StoreTaskStatus) {
	if kind == jobspec.KindPrune {
		store.Status.LastPrune = outcome
	} else {
		store.Status.LastCheck = outcome
	}
}

// deleteOldTaskJobs deletes the store's earlier Jobs of kind: a failed one
// is kept for its log until the next run.
func (r *OdooBackupStoreReconciler) deleteOldTaskJobs(ctx context.Context, store *odoov1.OdooBackupStore, kind string) error {
	return r.deleteJobsOfKind(ctx, store, kind, "")
}
