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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/schedule"
)

// Reasons of the BackupSchedule condition and the schedule's events.
const (
	ReasonScheduled         = "Scheduled"
	ReasonInvalidSchedule   = "InvalidSchedule"
	ReasonBackupScheduled   = "BackupScheduled"
	ReasonScheduleSkipped   = "ScheduledBackupSkipped"
	ReasonScheduleNameTaken = "ScheduledBackupNameTaken"
)

// scheduleSlack is added to the wait for the next scheduled time, so the
// reconcile that wakes for it finds it due rather than a moment early.
const scheduleSlack = time.Second

// scheduleBackups creates the OdooBackup of a scheduled time that has come,
// and returns how long until the next one. A time missed while the operator
// was down is taken once, however many were missed; one that comes while
// the previous scheduled backup has not finished is skipped. The backups go
// through the instance's queue like any other, so a scheduled time during
// an upgrade or restore runs after it.
func (r *OdooDeploymentReconciler) scheduleBackups(ctx context.Context, s *reconcileState, w instanceWork, now time.Time) (wait time.Duration, created bool, err error) {
	od := s.od
	st := &od.Status.Backup
	expr := strings.TrimSpace(od.Spec.Backup.Schedule)
	drillExpr := strings.TrimSpace(od.Spec.Backup.Drill.Schedule)
	tz := zoneOrUTC(od.Spec.Backup.TimeZone)
	if expr == "" && drillExpr == "" {
		*st = odoov1.OdooBackupScheduleStatus{LastDrill: st.LastDrill}
		meta.RemoveStatusCondition(&od.Status.Conditions, odoov1.ConditionBackupSchedule)
		return 0, false, nil
	}

	var problems []string

	if expr == "" {
		st.Schedule, st.LastScheduleTime, st.NextScheduleTime = "", nil, nil
	} else if sched, err := schedule.Parse(expr, tz); err != nil {
		problems = append(problems, fmt.Sprintf("spec.backup.schedule: %v", err))
		st.NextScheduleTime = nil
	} else {
		due := stepSchedule(sched, expr != st.Schedule || tz != st.TimeZone, &st.LastScheduleTime, &st.NextScheduleTime, now)
		st.Schedule = expr
		if !due.IsZero() {
			if created, err = r.createScheduledBackup(ctx, s, w, due, sched.Location()); err != nil {
				return 0, false, err
			}
		}
		if st.NextScheduleTime != nil {
			wait = st.NextScheduleTime.Sub(now) + scheduleSlack
		}
	}

	if drillExpr == "" {
		st.DrillSchedule, st.LastDrillScheduleTime, st.NextDrillTime = "", nil, nil
	} else if sched, err := schedule.Parse(drillExpr, tz); err != nil {
		problems = append(problems, fmt.Sprintf("spec.backup.drill.schedule: %v", err))
		st.NextDrillTime = nil
	} else {
		// The drill itself is started by the operation queue; here its
		// times are only kept.
		stepSchedule(sched, drillExpr != st.DrillSchedule || tz != st.TimeZone, &st.LastDrillScheduleTime, &st.NextDrillTime, now)
		st.DrillSchedule = drillExpr
		if st.NextDrillTime != nil {
			wait = minPositive(wait, st.NextDrillTime.Sub(now)+scheduleSlack)
		}
	}
	st.TimeZone = tz

	if len(problems) > 0 {
		msg := strings.Join(problems, "; ")
		if c := meta.FindStatusCondition(od.Status.Conditions, odoov1.ConditionBackupSchedule); c == nil || c.Message != msg {
			r.event(od, corev1.EventTypeWarning, ReasonInvalidSchedule, msg)
		}
		s.condition(odoov1.ConditionBackupSchedule, metav1.ConditionFalse, ReasonInvalidSchedule, msg)
		return wait, created, nil
	}
	var parts []string
	if expr != "" {
		parts = append(parts, fmt.Sprintf("backups %q", expr))
	}
	if drillExpr != "" {
		parts = append(parts, fmt.Sprintf("drills %q", drillExpr))
	}
	s.condition(odoov1.ConditionBackupSchedule, metav1.ConditionTrue, ReasonScheduled,
		fmt.Sprintf("%s in %s", strings.Join(parts, " and "), tz))
	return wait, created, nil
}

// stepSchedule moves one schedule's last and next times to now and returns
// the scheduled time that is due, if any. A new or changed schedule starts
// counting from now: nothing before it is due.
func stepSchedule(sched *schedule.Schedule, changed bool, last, next **metav1.Time, now time.Time) time.Time {
	if changed || *last == nil {
		t := metav1.NewTime(now)
		*last = &t
	}
	prev := (*last).Time
	due, n := sched.Step(&prev, now)
	if !due.IsZero() {
		t := metav1.NewTime(due)
		*last = &t
	}
	if n.IsZero() {
		*next = nil
	} else {
		t := metav1.NewTime(n)
		*next = &t
	}
	return due
}

// createScheduledBackup creates the OdooBackup for scheduled time due, named
// after it in the schedule's time zone, unless the previous scheduled backup
// is still to finish. Creating it again (after a crash before the status was
// written) finds the one already there.
func (r *OdooDeploymentReconciler) createScheduledBackup(ctx context.Context, s *reconcileState, w instanceWork, due time.Time, loc *time.Location) (bool, error) {
	od := s.od
	for i := range w.backups {
		if w.backups[i].Annotations[odoov1.AnnotationScheduledTime] == due.UTC().Format(time.RFC3339) {
			return false, nil // created by an earlier reconcile
		}
	}
	for i := range w.backups {
		b := &w.backups[i]
		if backupKind(b) != odoov1.BackupKindScheduled || !b.DeletionTimestamp.IsZero() {
			continue
		}
		switch b.Status.Phase {
		case "", odoov1.BackupPhasePending, odoov1.BackupPhaseWaitingForFilestoreCleanup, odoov1.BackupPhaseRunning:
			msg := fmt.Sprintf("Skipped the backup scheduled for %s: scheduled backup %s has not finished",
				due.In(loc).Format("2006-01-02 15:04 MST"), b.Name)
			r.event(od, corev1.EventTypeWarning, ReasonScheduleSkipped, msg)
			log.FromContext(ctx).Info(msg)
			return false, nil
		}
	}

	b := &odoov1.OdooBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      scheduledBackupName(od.Name, due.In(loc)),
			Namespace: od.Namespace,
			Labels: map[string]string{
				odoov1.LabelBackupKind:     odoov1.BackupKindScheduled,
				odoov1.LabelOdooDeployment: od.Name,
			},
			Annotations: map[string]string{odoov1.AnnotationScheduledTime: due.UTC().Format(time.RFC3339)},
		},
		Spec: odoov1.OdooBackupSpec{OdooDeployment: od.Name},
	}
	err := r.Create(ctx, b)
	if apierrors.IsAlreadyExists(err) {
		existing := &odoov1.OdooBackup{}
		if getErr := r.reader().Get(ctx, client.ObjectKeyFromObject(b), existing); getErr != nil {
			return false, fmt.Errorf("get OdooBackup %s: %w", b.Name, getErr)
		}
		if existing.Spec.OdooDeployment == od.Name && backupKind(existing) == odoov1.BackupKindScheduled {
			return true, nil
		}
		r.event(od, corev1.EventTypeWarning, ReasonScheduleNameTaken,
			fmt.Sprintf("Skipped the scheduled backup: an OdooBackup named %s already exists", b.Name))
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("create scheduled OdooBackup %s: %w", b.Name, err)
	}
	r.event(od, corev1.EventTypeNormal, ReasonBackupScheduled, fmt.Sprintf("Created scheduled backup %s", b.Name))
	log.FromContext(ctx).Info("Created scheduled backup", "backup", b.Name, "scheduledTime", due)
	return true, nil
}

// scheduledBackupName is <instance>-<yyyymmdd-hhmm> in the schedule's time
// zone, with seconds when the time has them (@every schedules). The name is
// also a label value on the backup's Job, so a long instance name is cut to
// keep it within 63 characters.
func scheduledBackupName(instance string, local time.Time) string {
	layout := "20060102-1504"
	if local.Second() != 0 {
		layout = "20060102-150405"
	}
	suffix := "-" + local.Format(layout)
	if max := 63 - len(suffix); len(instance) > max {
		instance = strings.TrimRight(instance[:max], "-.")
	}
	return instance + suffix
}

// zoneOrUTC is a schedule's time zone, UTC when unset.
func zoneOrUTC(tz string) string {
	if tz == "" {
		return "UTC"
	}
	return tz
}
