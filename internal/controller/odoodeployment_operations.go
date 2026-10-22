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
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/jobspec"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
	"github.com/MohanadAbugharbia/odoo-operator/internal/controller/reconcileloops"
	"github.com/MohanadAbugharbia/odoo-operator/internal/metrics"
	"github.com/MohanadAbugharbia/odoo-operator/pkg/utils"
)

// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoobackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoobackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoorestores,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoorestores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoorestores/finalizers,verbs=update
// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoobackupstores,verbs=get;list;watch

// Timings of backups and restores.
const (
	// cleanupWaitLimit is how long a backup waits for Odoo's filestore
	// cleanup before it runs anyway (the repair from the previous backup
	// covers what the cleanup deletes meanwhile).
	cleanupWaitLimit = 2 * time.Hour
	// cleanupRetry is how soon a deferred backup is tried again.
	cleanupRetry = 2 * time.Minute
	// maxRestoreRetryDelay caps the backoff of a restore that must be finished.
	maxRestoreRetryDelay = 15 * time.Minute
)

// Condition reasons of backups and restores.
const (
	ReasonBackupRunning       = "BackupRunning"
	ReasonBackupFailed        = "BackupFailed"
	ReasonBackupCompleted     = "BackupCompleted"
	ReasonBackupDeferred      = "BackupDeferred"
	ReasonRestoreRunning      = "RestoreRunning"
	ReasonRestoreFailed       = "RestoreFailed"
	ReasonRestoreCompleted    = "RestoreCompleted"
	ReasonRestoreIncomplete   = "RestoreIncomplete"
	ReasonRestoreRefused      = "RestoreRefused"
	ReasonCleanupFailed       = "RestoreCleanupFailed"
	ReasonBackupImageUnknown  = "BackupImageUnknown"
	ReasonWaitingForOperation = "WaitingForOperation"
	ReasonRetentionFailed     = "RetentionFailed"
	ReasonBackupExpired       = "BackupExpired"
	ReasonRestoreFromCopy     = "RestoreFromCopy"
	ReasonCopyFailed          = "BackupCopyFailed"
)

// instanceWork is every OdooBackup and OdooRestore of one instance, oldest first.
type instanceWork struct {
	backups  []odoov1.OdooBackup
	restores []odoov1.OdooRestore
}

// listWork lists the instance's backups and restores from the API server, so
// attempt counts and phases are never older than this reconciler's own last
// write. They are few per namespace, so they are filtered here instead of
// through an index.
func (r *OdooDeploymentReconciler) listWork(ctx context.Context, od *odoov1.OdooDeployment) (instanceWork, error) {
	var w instanceWork
	backups := &odoov1.OdooBackupList{}
	if err := r.reader().List(ctx, backups, client.InNamespace(od.Namespace)); err != nil {
		return w, fmt.Errorf("list OdooBackups: %w", err)
	}
	for _, b := range backups.Items {
		if b.Spec.OdooDeployment == od.Name {
			w.backups = append(w.backups, b)
		}
	}
	restores := &odoov1.OdooRestoreList{}
	if err := r.reader().List(ctx, restores, client.InNamespace(od.Namespace)); err != nil {
		return w, fmt.Errorf("list OdooRestores: %w", err)
	}
	for _, rs := range restores.Items {
		if rs.Spec.OdooDeployment == od.Name {
			w.restores = append(w.restores, rs)
		}
	}
	older := func(a, b metav1.ObjectMeta) bool {
		if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			return a.CreationTimestamp.Before(&b.CreationTimestamp)
		}
		return a.Name < b.Name
	}
	sort.Slice(w.backups, func(i, j int) bool { return older(w.backups[i].ObjectMeta, w.backups[j].ObjectMeta) })
	sort.Slice(w.restores, func(i, j int) bool { return older(w.restores[i].ObjectMeta, w.restores[j].ObjectMeta) })
	return w, nil
}

func (w instanceWork) restore(name string) *odoov1.OdooRestore {
	for i := range w.restores {
		if w.restores[i].Name == name {
			return &w.restores[i]
		}
	}
	return nil
}

func (w instanceWork) backup(name string) *odoov1.OdooBackup {
	for i := range w.backups {
		if w.backups[i].Name == name {
			return &w.backups[i]
		}
	}
	return nil
}

// nextRestore is the oldest restore still to run (or one a crash left
// started without the instance knowing).
func (w instanceWork) nextRestore() *odoov1.OdooRestore {
	for i := range w.restores {
		rs := &w.restores[i]
		switch rs.Status.Phase {
		case "", odoov1.RestorePhasePending, odoov1.RestorePhaseStopping, odoov1.RestorePhaseRestoring:
			if !rs.DeletionTimestamp.IsZero() && (rs.Status.Phase == "" || rs.Status.Phase == odoov1.RestorePhasePending) {
				continue // deleted before it started: nothing to do but let it go
			}
			return rs
		}
	}
	return nil
}

// nextBackup is the oldest backup still to take whose retry time has come,
// and when the next deferred one is due.
func (w instanceWork) nextBackup(now time.Time) (*odoov1.OdooBackup, time.Duration) {
	var wait time.Duration
	for i := range w.backups {
		b := &w.backups[i]
		if !b.DeletionTimestamp.IsZero() {
			continue
		}
		switch b.Status.Phase {
		case "", odoov1.BackupPhasePending, odoov1.BackupPhaseRunning, odoov1.BackupPhaseWaitingForFilestoreCleanup:
		default:
			continue
		}
		if b.Status.RetryAfter != nil && b.Status.RetryAfter.After(now) {
			if d := b.Status.RetryAfter.Sub(now); wait == 0 || d < wait {
				wait = d
			}
			continue
		}
		return b, 0
	}
	return nil, wait
}

// cleanupDue is a restore whose leftovers can be deleted now: the replaced
// data once keepPrevious has passed (or the OdooRestore is being deleted),
// and the journal of every finished restore.
func (w instanceWork) cleanupDue(now time.Time) (*odoov1.OdooRestore, time.Duration) {
	var wait time.Duration
	for i := range w.restores {
		rs := &w.restores[i]
		due, d := restoreCleanupDue(rs, now)
		if due {
			return rs, 0
		}
		if d > 0 && (wait == 0 || d < wait) {
			wait = d
		}
	}
	return nil, wait
}

// restoreCleanupDue tells whether the leftovers of restore rs can be deleted
// now, or else how long until they can (zero when never, or not yet known).
func restoreCleanupDue(rs *odoov1.OdooRestore, now time.Time) (bool, time.Duration) {
	st := &rs.Status
	if st.CleanedUp || st.Suffix == "" {
		return false, 0
	}
	if st.RetryAfter != nil && st.RetryAfter.After(now) {
		return false, st.RetryAfter.Sub(now)
	}
	switch st.Phase {
	case odoov1.RestorePhaseFailed:
		// Never when it changed live data: such a restore is finished, not failed.
		return !st.LiveChanged, 0
	case odoov1.RestorePhaseCompleted:
		if st.Previous.State != odoov1.PreviousKept || !rs.DeletionTimestamp.IsZero() {
			return true, 0
		}
		if st.Previous.KeptUntil != nil {
			if !st.Previous.KeptUntil.After(now) {
				return true, 0
			}
			return false, st.Previous.KeptUntil.Sub(now)
		}
	}
	return false, 0
}

// BackupImageLookup returns the backup image; the store reconciler uses it.
func (r *OdooDeploymentReconciler) BackupImageLookup(ctx context.Context) (string, error) {
	return r.backupImage(ctx)
}

// backupImage returns the backup Jobs' image: BackupImage, or the operator's
// own image with -backup appended (looked up once and kept).
func (r *OdooDeploymentReconciler) backupImage(ctx context.Context) (string, error) {
	r.imageMu.Lock()
	defer r.imageMu.Unlock()
	if r.BackupImage != "" {
		return r.BackupImage, nil
	}
	if r.OwnImage == nil {
		return "", errors.New("the operator has no backup image: set --backup-image")
	}
	own, err := r.OwnImage(ctx)
	if err != nil {
		return "", fmt.Errorf("look up the operator's image for the backup image: %w", err)
	}
	if own == "" {
		return "", errors.New("the operator has no backup image: set --backup-image")
	}
	r.BackupImage = BackupImageFor(own)
	log.FromContext(ctx).Info("backup image", "image", r.BackupImage)
	return r.BackupImage, nil
}

// BackupImageFor derives the backup image from the operator's own image:
// the same tag of the repository with "-backup" appended
// (ghcr.io/x/odoo-operator:1.2.3 → ghcr.io/x/odoo-operator-backup:1.2.3).
func BackupImageFor(operatorImage string) string {
	ref, digest, _ := strings.Cut(operatorImage, "@")
	slash := strings.LastIndex(ref, "/")
	repo, tag := ref, ""
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		repo, tag = ref[:colon], ref[colon:]
	}
	if tag == "" && digest != "" {
		// A digest belongs to the operator image only; fall back to latest.
		return repo + "-backup:latest"
	}
	return repo + "-backup" + tag
}

// patchBackupStatus applies mutate to b's status and patches it.
func (r *OdooDeploymentReconciler) patchBackupStatus(ctx context.Context, b *odoov1.OdooBackup, mutate func(*odoov1.OdooBackupStatus)) error {
	base := b.DeepCopy()
	mutate(&b.Status)
	if equality.Semantic.DeepEqual(base.Status, b.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, b, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch OdooBackup %s status: %w", b.Name, err)
	}
	return nil
}

// patchRestoreStatus applies mutate to rs's status and patches it.
func (r *OdooDeploymentReconciler) patchRestoreStatus(ctx context.Context, rs *odoov1.OdooRestore, mutate func(*odoov1.OdooRestoreStatus)) error {
	base := rs.DeepCopy()
	mutate(&rs.Status)
	if equality.Semantic.DeepEqual(base.Status, rs.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, rs, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch OdooRestore %s status: %w", rs.Name, err)
	}
	return nil
}

// setRestoreFinalizer adds or removes the restore finalizer.
func (r *OdooDeploymentReconciler) setRestoreFinalizer(ctx context.Context, rs *odoov1.OdooRestore, want bool) error {
	if controllerutil.ContainsFinalizer(rs, odoov1.RestoreFinalizer) == want {
		return nil
	}
	patched := rs.DeepCopy()
	if want {
		controllerutil.AddFinalizer(patched, odoov1.RestoreFinalizer)
	} else {
		controllerutil.RemoveFinalizer(patched, odoov1.RestoreFinalizer)
	}
	if err := r.Patch(ctx, patched, client.MergeFrom(rs)); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("patch OdooRestore %s finalizer: %w", rs.Name, err)
	}
	rs.ObjectMeta = patched.ObjectMeta
	return nil
}

func (r *OdooDeploymentReconciler) objectEvent(obj client.Object, eventType, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Event(obj, eventType, reason, message)
	}
}

// getStore reads a store and checks that the Secrets its Jobs mount exist
// with the keys they need, so a typo fails here instead of as a pod that
// never starts.
func (r *OdooDeploymentReconciler) getStore(ctx context.Context, namespace, name string) (*odoov1.OdooBackupStore, error) {
	store := &odoov1.OdooBackupStore{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, store); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("OdooBackupStore %q does not exist in namespace %s", name, namespace)
		}
		return nil, err
	}
	if err := checkStoreSecrets(ctx, r.Client, store); err != nil {
		return nil, err
	}
	return store, nil
}

// errStoreReadOnly: a backup was to be written to a readOnly store.
var errStoreReadOnly = errors.New("is readOnly")

// getWritableStore is getStore for a store backups are written to.
func (r *OdooDeploymentReconciler) getWritableStore(ctx context.Context, namespace, name string) (*odoov1.OdooBackupStore, error) {
	store, err := r.getStore(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	if store.Spec.ReadOnly {
		return nil, fmt.Errorf("OdooBackupStore %s %w: backups are only read from it; "+
			"remove it from spec.backup.stores", name, errStoreReadOnly)
	}
	return store, nil
}

// getBackupStore is getWritableStore for the store a backup is taken into,
// the first in spec.backup.stores: a zip store only takes copies.
func (r *OdooDeploymentReconciler) getBackupStore(ctx context.Context, namespace, name string) (*odoov1.OdooBackupStore, error) {
	store, err := r.getWritableStore(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	if store.Spec.Format == odoov1.BackupStoreFormatZip {
		return nil, fmt.Errorf("OdooBackupStore %s keeps zip files and only takes copies; "+
			"list a restic store first in spec.backup.stores", name)
	}
	return store, nil
}

// checkStoreSecrets checks the Secrets a store names.
func checkStoreSecrets(ctx context.Context, c client.Reader, store *odoov1.OdooBackupStore) error {
	for name, keys := range jobspec.StoreSecrets(store) {
		secret := &corev1.Secret{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: store.Namespace, Name: name}, secret); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("secret %q of OdooBackupStore %s does not exist", name, store.Name)
			}
			return err
		}
		for _, k := range keys {
			if !slices.ContainsFunc(strings.Split(k, "|"), func(k string) bool { _, ok := secret.Data[k]; return ok }) {
				return fmt.Errorf("secret %q of OdooBackupStore %s has no key %s", name, store.Name,
					`"`+strings.ReplaceAll(k, "|", `" or "`)+`"`)
			}
		}
	}
	return nil
}

// jobParams are the common inputs of the instance's backup Jobs.
func (r *OdooDeploymentReconciler) jobParams(ctx context.Context, od *odoov1.OdooDeployment, store *odoov1.OdooBackupStore) (jobspec.Params, error) {
	image, err := r.backupImage(ctx)
	if err != nil {
		return jobspec.Params{}, err
	}
	return jobspec.Params{
		Image:           image,
		Instance:        od,
		Database:        od.Status.Database.Name,
		Store:           store,
		OperatorVersion: r.Version,
	}, nil
}

// pvcAccess tells how the data volume can be shared: rwx when any node can
// mount it, rwop when only one pod can.
func (r *OdooDeploymentReconciler) pvcAccess(ctx context.Context, od *odoov1.OdooDeployment) (rwx, rwop bool, err error) {
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: od.Namespace, Name: od.GetPvcName()}, pvc); err != nil {
		return false, false, fmt.Errorf("get the data volume claim: %w", err)
	}
	modes := pvc.Status.AccessModes
	if len(modes) == 0 {
		modes = pvc.Spec.AccessModes
	}
	for _, m := range modes {
		switch m {
		case corev1.ReadWriteMany:
			rwx = true
		case corev1.ReadWriteOncePod:
			rwop = true
		}
	}
	return rwx, rwop, nil
}

// affinityToOdoo pins a Job that mounts the data volume next to a running
// Odoo pod, since a ReadWriteOnce volume is attached to one node at a time.
func (r *OdooDeploymentReconciler) affinityToOdoo(ctx context.Context, od *odoov1.OdooDeployment) (*corev1.Affinity, error) {
	rwx, _, err := r.pvcAccess(ctx, od)
	if err != nil || rwx {
		return nil, err
	}
	pods := &corev1.PodList{}
	if err := r.reader().List(ctx, pods, client.InNamespace(od.Namespace), client.MatchingLabels(od.GetServiceSelectorLabels())); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	for _, p := range pods.Items {
		if p.Spec.NodeName == "" || !p.DeletionTimestamp.IsZero() {
			continue
		}
		return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchFields: []corev1.NodeSelectorRequirement{{
						Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{p.Spec.NodeName},
					}},
				}},
			},
		}}, nil
	}
	return nil, nil
}

// createJob creates job owned by the instance, or returns the one a
// previous reconcile created under the same name.
func (r *OdooDeploymentReconciler) createJob(ctx context.Context, od *odoov1.OdooDeployment, job *batchv1.Job) error {
	if err := controllerutil.SetControllerReference(od, job, r.Scheme); err != nil {
		return err
	}
	err := r.Create(ctx, job)
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create job %s: %w", job.Name, err)
	}
	log.FromContext(ctx).Info("Created job", "job", job.Name, "kind", job.Labels[odoov1.LabelJobKind])
	return nil
}

// operationJobKinds are the Job kinds of backups, restores and cleanups, of
// which an instance runs one at a time.
var operationJobKinds = map[string]bool{
	jobspec.KindBackup: true, jobspec.KindRestore: true, jobspec.KindCleanup: true, jobspec.KindDrill: true,
}

// sameOperation reports whether two Job names differ at most in their
// attempt suffix.
func sameOperation(a, b string) bool {
	trim := func(n string) string {
		if i := strings.LastIndexByte(n, '-'); i > 0 {
			return n[:i]
		}
		return n
	}
	return trim(a) == trim(b)
}

// startOperationJob creates job unless one of the instance's operation Jobs
// is still running, read from the API server rather than the cache: a status
// write that was lost, or a cache that lags it, must never start a second
// Job beside a running one. It returns the Job to follow: job's own name, a
// running attempt of the same operation, or "" to wait for another one.
func (r *OdooDeploymentReconciler) startOperationJob(ctx context.Context, s *reconcileState, job *batchv1.Job) (string, error) {
	od := s.od
	jobs := &batchv1.JobList{}
	if err := r.reader().List(ctx, jobs, client.InNamespace(od.Namespace),
		client.MatchingLabels{odoov1.LabelOdooDeployment: od.Name}); err != nil {
		return "", fmt.Errorf("list jobs: %w", err)
	}
	for i := range jobs.Items {
		j := &jobs.Items[i]
		if !operationJobKinds[j.Labels[odoov1.LabelJobKind]] || !metav1.IsControlledBy(j, od) || jobFinished(j) {
			continue
		}
		if sameOperation(j.Name, job.Name) {
			log.FromContext(ctx).Info("Following running job", "job", j.Name)
			return j.Name, nil
		}
		s.ready(metav1.ConditionFalse, ReasonWaitingForOperation, "waiting for job "+j.Name+" to finish")
		return "", nil
	}
	if err := s.holdForJob(ctx); err != nil {
		return "", err
	}
	if err := r.createJob(ctx, od, job); err != nil {
		return "", err
	}
	return job.Name, nil
}

// jobFinished reports whether a Job completed, failed or is being deleted.
func jobFinished(j *batchv1.Job) bool {
	if !j.DeletionTimestamp.IsZero() {
		return true
	}
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// jobOutcome is a finished (or vanished) Job and its Result.
type jobOutcome struct {
	state   reconcileloops.JobState
	job     *batchv1.Job
	result  result.Result
	decoded bool
	// detail describes a Job that failed without a Result.
	detail string
	// startMessage is set while the Job's pod cannot start: it cannot pull
	// its image, a quota keeps it from being created, it cannot be
	// scheduled, a container cannot be created, or it waits long for a
	// volume.
	startMessage string
	// mainStarted is false only when no pod of the finished Job got as far
	// as its main container (a step before it failed, or no pod ran at
	// all), and every pod the Job counted is still there to show it.
	mainStarted bool
}

// observeJob reads the state of a backup Job and, once it has finished, the
// Result its newest pod wrote. Only the newest pod speaks for the Job: an
// older attempt's result is stale once another pod ran after it, so when the
// newest pod's main container did not finish (it never started, still runs,
// or the pod is gone) the outcome has no Result.
func (r *OdooDeploymentReconciler) observeJob(ctx context.Context, namespace, name string) (jobOutcome, error) {
	od := &odoov1.OdooDeployment{}
	od.Namespace = namespace
	od.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{Name: name, Namespace: namespace}
	obs, err := reconcileloops.ObserveMaintenanceJob(ctx, r.Client, r.reader(), od)
	if err != nil {
		return jobOutcome{}, err
	}
	out := jobOutcome{state: obs.State, job: obs.Job, detail: obs.FailureMessage,
		startMessage: orDefault(obs.ImagePullMessage, obs.StartMessage)}
	if obs.QuotaMessage != "" {
		out.detail = obs.QuotaMessage
		out.startMessage = obs.QuotaMessage
	}
	if obs.State == reconcileloops.JobStateActive && obs.NeverStarted && r.now().Sub(obs.StartedAt) > jobStartLimit {
		return r.giveUpJob(ctx, out)
	}
	if obs.State != reconcileloops.JobStateSucceeded && obs.State != reconcileloops.JobStateFailed {
		return out, nil
	}
	pods := &corev1.PodList{}
	if err := r.reader().List(ctx, pods, client.InNamespace(namespace), client.MatchingLabels{"job-name": name}); err != nil {
		return out, fmt.Errorf("list pods of job %s: %w", name, err)
	}
	out.mainStarted = obs.Job != nil && len(pods.Items) < int(obs.Job.Status.Failed+obs.Job.Status.Succeeded)
	for i := range pods.Items {
		if mainMayHaveRun(&pods.Items[i]) {
			out.mainStarted = true
		}
	}
	newest := newestPod(pods.Items)
	if newest == nil {
		return out, nil
	}
	var main *corev1.ContainerStateTerminated
	for _, cs := range newest.Status.ContainerStatuses {
		if cs.Name == jobspec.ContainerName {
			main = cs.State.Terminated
		}
	}
	if main == nil {
		if why := podFailureText(newest); why != "" && out.detail != "" {
			out.detail = why + "; " + out.detail
		} else if why != "" {
			out.detail = why
		}
		return out, nil
	}
	res, err := result.Decode(main.Message)
	if err != nil {
		if out.detail == "" {
			out.detail = fmt.Sprintf("%s (exit code %d)", main.Reason, main.ExitCode)
		}
		return out, nil
	}
	out.result, out.decoded = res, true
	return out, nil
}

// jobStartLimit is how long an operation Job may go without any of its pods
// starting a container before the operator deletes it and counts it failed.
// Such a Job waits on something only a person fixes (a node that fits, a
// missing Secret, a volume attached elsewhere, an image that cannot be
// pulled), and a restore or an offline backup keeps Odoo stopped meanwhile.
const jobStartLimit = 15 * time.Minute

// giveUpJob deletes an operation Job none of whose pods ever started a
// container in jobStartLimit, and reports it failed without a result and
// without its main container having run: nothing of it ran, so it changed
// nothing. The deletion is in the foreground, so the Job's pods are marked
// for deletion before the Job goes and none of them starts a container
// later: the kubelet does not start a pod being deleted.
func (r *OdooDeploymentReconciler) giveUpJob(ctx context.Context, out jobOutcome) (jobOutcome, error) {
	job := out.job
	why := out.startMessage
	if why == "" {
		why = "its pods are pending; kubectl describe them to see why"
	}
	log.FromContext(ctx).Info("Deleting a job none of whose pods started", "job", job.Name, "limit", jobStartLimit, "why", why)
	if job.DeletionTimestamp.IsZero() {
		propagation := metav1.DeletePropagationForeground
		err := r.Delete(ctx, job, &client.DeleteOptions{
			PropagationPolicy: &propagation, Preconditions: &metav1.Preconditions{UID: &job.UID},
		})
		if err != nil && !apierrors.IsNotFound(err) {
			return out, fmt.Errorf("delete job %s: %w", job.Name, err)
		}
	}
	out.state = reconcileloops.JobStateFailed
	out.mainStarted = false
	out.startMessage = ""
	out.detail = fmt.Sprintf("started no container within %s, so the operator deleted it (%s)", jobStartLimit, why)
	return out, nil
}

// newestPod is the pod created last, the Job's latest attempt; pods created
// in the same second are told apart by name, so the choice is stable.
func newestPod(pods []corev1.Pod) *corev1.Pod {
	var newest *corev1.Pod
	for i := range pods {
		p := &pods[i]
		if newest == nil || newest.CreationTimestamp.Before(&p.CreationTimestamp) ||
			(newest.CreationTimestamp.Equal(&p.CreationTimestamp) && p.Name > newest.Name) {
			newest = p
		}
	}
	return newest
}

// mainMayHaveRun reports whether the pod's main container may have started.
// Only a pod that shows it never got that far says no: its main container
// is waiting and never ran before, or it has no status for it while the pod
// is pending or failed and did not get through its steps (init containers).
func mainMayHaveRun(p *corev1.Pod) bool {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name == jobspec.ContainerName {
			return cs.State.Waiting == nil || cs.LastTerminationState.Terminated != nil || cs.LastTerminationState.Running != nil
		}
	}
	if p.Status.Phase != corev1.PodPending && p.Status.Phase != corev1.PodFailed {
		return true
	}
	if len(p.Status.InitContainerStatuses) < len(p.Spec.InitContainers) {
		return false
	}
	for _, cs := range p.Status.InitContainerStatuses {
		if t := cs.State.Terminated; t == nil || t.ExitCode != 0 {
			return false
		}
	}
	// Every step succeeded but the main container has no status yet.
	return len(p.Spec.InitContainers) > 0
}

// podFailureText says why a pod's main container did not finish, from what
// the kubelet reported: a step (init container) that failed or could not
// start, a main container that could not start, or the pod's own reason
// (evicted, deadline exceeded). It is "" when nothing says why.
func podFailureText(p *corev1.Pod) string {
	for _, cs := range p.Status.InitContainerStatuses {
		if t := cs.State.Terminated; t != nil && t.ExitCode != 0 {
			return fmt.Sprintf("failed in step %s: %s (exit code %d)", cs.Name, orDefault(t.Reason, "Error"), t.ExitCode)
		}
		if w := cs.State.Waiting; w != nil && w.Reason != "" && w.Reason != "PodInitializing" {
			return joinDetail(fmt.Sprintf("step %s could not start: %s", cs.Name, w.Reason), w.Message)
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if w := cs.State.Waiting; cs.Name == jobspec.ContainerName && w != nil && w.Reason != "" && w.Reason != "PodInitializing" {
			return joinDetail(fmt.Sprintf("step %s could not start: %s", cs.Name, w.Reason), w.Message)
		}
	}
	if p.Status.Reason != "" {
		return joinDetail("pod "+p.Name+" "+p.Status.Reason, p.Status.Message)
	}
	return ""
}

// joinDetail joins two parts of a failure description, leaving out empty ones.
func joinDetail(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + ": " + b
}

// failureText describes a failed Job for a status message.
func failureText(out jobOutcome) string {
	if out.decoded && out.result.Error != "" {
		return fmt.Sprintf("failed at step %s: %s", out.result.Step, out.result.Error)
	}
	if out.detail != "" {
		return out.detail
	}
	return "the Job failed without reporting why"
}

// ---- the operation in flight -------------------------------------------------

// observeOperation drives status.currentOperation. done is true once the
// instance is free for other work.
func (r *OdooDeploymentReconciler) observeOperation(ctx context.Context, s *reconcileState, w instanceWork, configHash string) (ctrl.Result, bool, error) {
	op := s.od.Status.CurrentOperation
	switch op.Kind {
	case odoov1.OperationBackup:
		b := w.backup(op.Name)
		if b == nil {
			return r.abandonOperation(ctx, s, "OdooBackup "+op.Name+" was deleted")
		}
		return r.driveBackup(ctx, s, w, b, configHash)
	case odoov1.OperationRestore:
		rs := w.restore(op.Name)
		if rs == nil {
			return r.abandonOperation(ctx, s, "OdooRestore "+op.Name+" was deleted")
		}
		return r.driveRestore(ctx, s, w, rs, configHash)
	case odoov1.OperationCleanup:
		rs := w.restore(op.Name)
		if rs == nil {
			return r.abandonOperation(ctx, s, "OdooRestore "+op.Name+" was deleted")
		}
		return r.driveCleanup(ctx, s, rs, configHash)
	case odoov1.OperationDrill:
		return r.driveDrill(ctx, s, w)
	}
	s.od.Status.CurrentOperation = odoov1.OperationStatus{}
	return ctrl.Result{}, true, nil
}

// abandonOperation ends an operation whose object is gone, once its Job (if
// any) is no longer running.
func (r *OdooDeploymentReconciler) abandonOperation(ctx context.Context, s *reconcileState, why string) (ctrl.Result, bool, error) {
	op := s.od.Status.CurrentOperation
	if op.Job != "" {
		out, err := r.observeJob(ctx, s.od.Namespace, op.Job)
		if err != nil {
			return ctrl.Result{}, false, err
		}
		if out.state == reconcileloops.JobStateActive {
			s.ready(metav1.ConditionFalse, ReasonWaitingForOperation, why+"; waiting for job "+op.Job+" to finish")
			return ctrl.Result{RequeueAfter: 15 * time.Second}, false, nil
		}
		mayHaveChanged := out.result.LiveChanged && !out.result.OK
		if !out.decoded {
			mayHaveChanged = out.mainStarted
		}
		if op.Kind == odoov1.OperationRestore && mayHaveChanged {
			// The restore may have renamed the live data and nobody can
			// finish it any more: keep Odoo stopped for a person to look.
			msg := fmt.Sprintf("%s while job %s had not finished the swap; Odoo stays stopped. "+
				"Check the databases and filestore folders with suffix in the job log, then clear status.currentOperation", why, op.Job)
			s.degraded(ReasonRestoreIncomplete, msg)
			s.ready(metav1.ConditionFalse, ReasonRestoreIncomplete, msg)
			s.od.Status.Phase = odoov1.PhaseFailed
			return ctrl.Result{RequeueAfter: 5 * time.Minute}, false, nil
		}
	}
	log.FromContext(ctx).Info("Operation abandoned", "kind", op.Kind, "name", op.Name, "reason", why)
	s.od.Status.CurrentOperation = odoov1.OperationStatus{}
	return ctrl.Result{RequeueAfter: time.Second}, false, nil
}

// stopOdoo scales the Deployment to zero and reports whether its pods are gone.
func (r *OdooDeploymentReconciler) stopOdoo(ctx context.Context, s *reconcileState, configHash string) (bool, error) {
	od := s.od
	image := od.Status.AppliedImage
	if image == "" {
		// Odoo never ran (a restore onto a new instance, before its init Job):
		// there is no image to write a Deployment with, and nothing to stop
		// unless a Deployment exists anyway, which keeps its own image.
		dep := &appsv1.Deployment{}
		err := r.Get(ctx, types.NamespacedName{Namespace: od.Namespace, Name: od.Name}, dep)
		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			return false, err
		case len(dep.Spec.Template.Spec.Containers) > 0:
			image = dep.Spec.Template.Spec.Containers[0].Image
		}
	}
	if image != "" {
		if _, err := reconcileloops.EnsureDeployment(ctx, r.Client, r.Scheme, od, image, 0, configHash); err != nil {
			return false, err
		}
	}
	running, err := r.countPods(ctx, od.Namespace, od.GetServiceSelectorLabels())
	if err != nil {
		return false, err
	}
	return running == 0, nil
}

// ---- backups -------------------------------------------------------------------

// startBackup claims the instance for backup b.
func (r *OdooDeploymentReconciler) startBackup(ctx context.Context, s *reconcileState, w instanceWork, b *odoov1.OdooBackup, configHash string) (ctrl.Result, error) {
	od := s.od
	fail := func(msg string) (ctrl.Result, error) {
		r.objectEvent(b, corev1.EventTypeWarning, ReasonBackupFailed, msg)
		metrics.BackupFinished(backupKind(b), metrics.BackupFailed, 0)
		err := r.patchBackupStatus(ctx, b, func(st *odoov1.OdooBackupStatus) {
			st.Phase = odoov1.BackupPhaseFailed
			st.Message = msg
			now := metav1.Now()
			st.CompletedAt = &now
		})
		return ctrl.Result{RequeueAfter: time.Second}, err
	}
	if len(od.Spec.Backup.Stores) == 0 {
		return fail(fmt.Sprintf("OdooDeployment %s has no spec.backup.stores", od.Name))
	}
	if _, err := r.getBackupStore(ctx, od.Namespace, od.Spec.Backup.Stores[0]); err != nil {
		if errors.Is(err, errStoreReadOnly) {
			r.event(od, corev1.EventTypeWarning, odoov1.ReasonStoreReadOnly, err.Error())
		}
		return fail(err.Error())
	}
	// A backup before an upgrade always stops Odoo: the upgrade stops it
	// anyway, and nothing written after the backup is lost by going back.
	stop := od.Spec.Backup.ConsistencyValue() == odoov1.BackupConsistencyOffline ||
		backupKind(b) == odoov1.BackupKindPreUpgrade
	if !stop {
		_, rwop, err := r.pvcAccess(ctx, od)
		if err != nil {
			return s.fail(odoov1.ReasonReconcileFailed, err)
		}
		stop = rwop
	}
	now := metav1.Now()
	od.Status.CurrentOperation = odoov1.OperationStatus{
		Kind: odoov1.OperationBackup, Name: b.Name, StoppedOdoo: stop, StartedAt: &now,
	}
	log.FromContext(ctx).Info("Starting backup", "backup", b.Name, "offline", stop)
	result, _, err := r.driveBackup(ctx, s, w, b, configHash)
	return result, err
}

func backupKind(b *odoov1.OdooBackup) string {
	if k := b.Labels[odoov1.LabelBackupKind]; k != "" {
		return k
	}
	return odoov1.BackupKindManual
}

// driveBackup moves the backup in status.currentOperation along.
//
//nolint:gocyclo // one switch over the backup job outcomes
func (r *OdooDeploymentReconciler) driveBackup(ctx context.Context, s *reconcileState, w instanceWork, b *odoov1.OdooBackup, configHash string) (ctrl.Result, bool, error) {
	od := s.od
	op := &od.Status.CurrentOperation
	if op.StoppedOdoo && op.Job == "" && !op.StoreReached {
		return r.reachStore(ctx, s, b)
	}
	if op.StoppedOdoo {
		od.Status.Phase = odoov1.PhaseBackingUp
	}
	if op.Job == "" {
		if op.StoppedOdoo {
			stopped, err := r.stopOdoo(ctx, s, configHash)
			if err != nil {
				res, err := s.fail(odoov1.ReasonDeploymentFailed, err)
				return res, false, err
			}
			if !stopped {
				s.ready(metav1.ConditionFalse, odoov1.ReasonWaitingForPodsToStop, "stopping Odoo for an offline backup")
				return ctrl.Result{RequeueAfter: 10 * time.Second}, false, nil
			}
		}
		store, err := r.getBackupStore(ctx, od.Namespace, od.Spec.Backup.Stores[0])
		if err != nil {
			return r.finishBackupFailed(ctx, s, b, err.Error())
		}
		params, err := r.jobParams(ctx, od, store)
		if err != nil {
			s.degraded(ReasonBackupImageUnknown, err.Error())
			return ctrl.Result{RequeueAfter: time.Minute}, false, nil
		}
		if !op.StoppedOdoo {
			if params.Affinity, err = r.affinityToOdoo(ctx, od); err != nil {
				res, err := s.fail(odoov1.ReasonReconcileFailed, err)
				return res, false, err
			}
		}
		params.Offline = op.StoppedOdoo
		params.Retention = backupRetention(od, w)
		var skipped []odoov1.BackupCopy
		params.Copies, skipped = r.copyStores(ctx, od)
		attempt := b.Status.Attempts + 1
		wait := od.Spec.Backup.ConsistencyValue() == odoov1.BackupConsistencyOnline && !op.StoppedOdoo
		if b.Status.WaitingSince != nil && time.Since(b.Status.WaitingSince.Time) > cleanupWaitLimit {
			wait = false
		}
		job, err := jobspec.BackupJob(params, b, backupKind(b), attempt, wait)
		if err != nil {
			return r.finishBackupFailed(ctx, s, b, err.Error())
		}
		started, err := r.startOperationJob(ctx, s, &job)
		if err != nil {
			res, err := s.fail(odoov1.ReasonJobCreationFailed, err)
			return res, false, err
		}
		if started != job.Name {
			op.Job = started
			return ctrl.Result{RequeueAfter: 10 * time.Second}, false, nil
		}
		err = r.patchBackupStatus(ctx, b, func(st *odoov1.OdooBackupStatus) {
			st.Phase = odoov1.BackupPhaseRunning
			st.Message = "job " + job.Name + " is taking the backup"
			st.Job = job.Name
			st.Attempts = attempt
			st.Store = store.Name
			st.Repository = layout.Repository(od.Namespace, od.Name)
			st.Consistency = string(od.Spec.Backup.ConsistencyValue())
			if op.StoppedOdoo {
				st.Consistency = string(odoov1.BackupConsistencyOffline)
			}
			st.RetryAfter = nil
			if st.StartedAt == nil {
				st.StartedAt = op.StartedAt
			}
			st.Copies = skipped
		})
		if err != nil {
			return ctrl.Result{}, false, err
		}
		op.Job = job.Name
		r.objectEvent(b, corev1.EventTypeNormal, ReasonBackupRunning, "job "+job.Name+" started")
	}

	out, err := r.observeJob(ctx, od.Namespace, op.Job)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	switch out.state {
	case reconcileloops.JobStateMissing:
		op.Job = ""
		return ctrl.Result{RequeueAfter: time.Second}, false, nil
	case reconcileloops.JobStateActive:
		if op.StoppedOdoo {
			s.ready(metav1.ConditionFalse, ReasonBackupRunning, "Odoo is stopped for backup "+b.Name)
		}
		if out.startMessage != "" {
			msg := "job " + op.Job + " cannot start: " + out.startMessage
			_ = r.patchBackupStatus(ctx, b, func(st *odoov1.OdooBackupStatus) { st.Message = msg })
			s.degraded(odoov1.ReasonJobCannotStart, fmt.Sprintf("backup %s: %s; a Job none of whose pods starts a container "+
				"within %s is given up", b.Name, msg, jobStartLimit))
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, false, nil
	}

	took := time.Duration(0)
	if b.Status.StartedAt != nil {
		took = time.Since(b.Status.StartedAt.Time).Round(time.Second)
	}
	switch {
	case out.state == reconcileloops.JobStateSucceeded && out.decoded && out.result.Deferred:
		reason := strings.Join(out.result.Warnings, "; ")
		err := r.patchBackupStatus(ctx, b, func(st *odoov1.OdooBackupStatus) {
			st.Phase = odoov1.BackupPhaseWaitingForFilestoreCleanup
			st.Message = reason + "; the backup waits for it to finish"
			if st.WaitingSince == nil {
				now := metav1.Now()
				st.WaitingSince = &now
			}
			retry := metav1.NewTime(time.Now().Add(cleanupRetry))
			st.RetryAfter = &retry
			st.Job = ""
		})
		if err != nil {
			return ctrl.Result{}, false, err
		}
		if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, out.job); err != nil {
			return ctrl.Result{}, false, err
		}
		r.objectEvent(b, corev1.EventTypeNormal, ReasonBackupDeferred, reason)
		od.Status.CurrentOperation = odoov1.OperationStatus{}
		return ctrl.Result{RequeueAfter: cleanupRetry}, true, nil

	case out.state == reconcileloops.JobStateSucceeded && out.decoded && out.result.OK:
		res := out.result
		phase := odoov1.BackupPhaseCompleted
		msg := fmt.Sprintf("snapshot %s: %s of database, %d files (%s) in the filestore",
			short(res.SnapshotID), bytesText(res.DatabaseBytes), res.FilestoreFiles, bytesText(res.FilestoreBytes))
		if res.MissingFiles > 0 || len(res.Warnings) > 0 {
			phase = odoov1.BackupPhaseCompletedWithWarnings
			msg += "; " + strings.Join(res.Warnings, "; ")
		}
		copies := mergeCopies(b.Status.Copies, res.Copies)
		var failedCopies []string
		for _, c := range copies {
			if c.Error != "" {
				failedCopies = append(failedCopies, c.Store)
			}
		}
		if len(failedCopies) > 0 {
			phase = odoov1.BackupPhasePartiallyCompleted
			msg += fmt.Sprintf("; the copy to %s failed", strings.Join(failedCopies, ", "))
		}
		var warnings []string
		if res.RetentionError != "" {
			warnings = append(warnings, "retention did not run: "+res.RetentionError)
		}
		err := r.patchBackupStatus(ctx, b, func(st *odoov1.OdooBackupStatus) {
			st.Phase = phase
			st.Message = msg
			st.Warnings = warnings
			st.Copies = copies
			st.SnapshotID = res.SnapshotID
			st.OdooVersion = res.OdooVersion
			st.PgServerVersion = res.PgServerVersion
			st.Image = od.Status.AppliedImage
			st.DatabaseBytes = res.DatabaseBytes
			st.FilestoreBytes = res.FilestoreBytes
			st.FilestoreFiles = res.FilestoreFiles
			st.AddedBytes = res.AddedBytes
			st.RepairedFiles = int32(res.RepairedFiles)
			st.MissingFiles = int32(res.MissingFiles)
			st.MissingFileNames = res.MissingNames
			st.RetryAfter = nil
			now := metav1.Now()
			st.CompletedAt = &now
		})
		if err != nil {
			return ctrl.Result{}, false, err
		}
		// Before the Job goes: its result is how a failed delete is retried.
		if err := r.deleteForgotten(ctx, od, w, b.Status.Store, res.Forgotten); err != nil {
			return ctrl.Result{}, false, err
		}
		if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, out.job); err != nil {
			return ctrl.Result{}, false, err
		}
		eventType := corev1.EventTypeNormal
		if phase != odoov1.BackupPhaseCompleted {
			eventType = corev1.EventTypeWarning
		}
		r.objectEvent(b, eventType, ReasonBackupCompleted, msg)
		for _, warning := range warnings {
			r.objectEvent(b, corev1.EventTypeWarning, ReasonRetentionFailed, warning)
		}
		for _, c := range copies {
			if c.Error != "" {
				r.objectEvent(b, corev1.EventTypeWarning, ReasonCopyFailed, fmt.Sprintf("copy to %s failed: %s", c.Store, c.Error))
			}
		}
		metrics.BackupFinished(backupKind(b), metrics.BackupSucceeded, took)
		od.Status.CurrentOperation = odoov1.OperationStatus{}
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	msg := fmt.Sprintf("job %s %s; inspect with: kubectl -n %s logs job/%s", op.Job, failureText(out), od.Namespace, op.Job)
	return r.finishBackupFailed(ctx, s, b, msg)
}

// reachStore checks, before an offline backup stops Odoo, that the backup's
// store can be reached. An unreachable store fails the backup within
// minutes, with Odoo still serving, where restic would retry it for a
// quarter of an hour with Odoo stopped.
func (r *OdooDeploymentReconciler) reachStore(ctx context.Context, s *reconcileState, b *odoov1.OdooBackup) (ctrl.Result, bool, error) {
	od := s.od
	store, err := r.getStore(ctx, od.Namespace, od.Spec.Backup.Stores[0])
	if err != nil {
		return r.finishBackupFailed(ctx, s, b, err.Error())
	}
	name := jobspec.ReachJobName(od, b)
	out, err := r.observeJob(ctx, od.Namespace, name)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	waiting := fmt.Sprintf("checking that store %s can be reached before stopping Odoo for backup %s", store.Name, b.Name)
	switch out.state {
	case reconcileloops.JobStateMissing:
		image, err := r.backupImage(ctx)
		if err != nil {
			s.degraded(ReasonBackupImageUnknown, err.Error())
			return ctrl.Result{RequeueAfter: time.Minute}, false, nil
		}
		job, err := jobspec.ReachJob(image, store, od, b)
		if err != nil {
			return r.finishBackupFailed(ctx, s, b, err.Error())
		}
		if err := controllerutil.SetControllerReference(od, &job, r.Scheme); err != nil {
			return ctrl.Result{}, false, err
		}
		if err := s.holdForJob(ctx); err != nil {
			return ctrl.Result{}, false, err
		}
		if err := r.Create(ctx, &job); err != nil && !apierrors.IsAlreadyExists(err) {
			res, err := s.fail(odoov1.ReasonJobCreationFailed, fmt.Errorf("create job %s: %w", name, err))
			return res, false, err
		}
		_ = r.patchBackupStatus(ctx, b, func(st *odoov1.OdooBackupStatus) { st.Message = waiting })
		return ctrl.Result{RequeueAfter: 5 * time.Second}, false, nil
	case reconcileloops.JobStateActive:
		if out.startMessage != "" {
			waiting = "job " + name + " cannot start: " + out.startMessage
			_ = r.patchBackupStatus(ctx, b, func(st *odoov1.OdooBackupStatus) { st.Message = waiting })
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, false, nil
	case reconcileloops.JobStateSucceeded:
		if out.decoded && out.result.OK {
			if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, out.job); err != nil {
				return ctrl.Result{}, false, err
			}
			s.od.Status.CurrentOperation.StoreReached = true
			return ctrl.Result{RequeueAfter: time.Second}, false, nil
		}
	}
	// The Job is kept for its log until its TTL ends.
	return r.finishBackupFailed(ctx, s, b, fmt.Sprintf(
		"store %s cannot be reached, so Odoo was not stopped: job %s %s; inspect with: kubectl -n %s logs job/%s",
		store.Name, name, failureText(out), od.Namespace, name))
}

// finishBackupFailed marks the backup failed and frees the instance.
func (r *OdooDeploymentReconciler) finishBackupFailed(ctx context.Context, s *reconcileState, b *odoov1.OdooBackup, msg string) (ctrl.Result, bool, error) {
	took := time.Duration(0)
	if b.Status.StartedAt != nil {
		took = time.Since(b.Status.StartedAt.Time).Round(time.Second)
	}
	err := r.patchBackupStatus(ctx, b, func(st *odoov1.OdooBackupStatus) {
		st.Phase = odoov1.BackupPhaseFailed
		st.Message = msg
		now := metav1.Now()
		st.CompletedAt = &now
	})
	if err != nil {
		return ctrl.Result{}, false, err
	}
	r.objectEvent(b, corev1.EventTypeWarning, ReasonBackupFailed, msg)
	r.event(s.od, corev1.EventTypeWarning, ReasonBackupFailed, "backup "+b.Name+": "+msg)
	metrics.BackupFinished(backupKind(b), metrics.BackupFailed, took)
	s.od.Status.CurrentOperation = odoov1.OperationStatus{}
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// ---- restores --------------------------------------------------------------------

// restoreSource is what a restore puts in place.
type restoreSource struct {
	store    *odoov1.OdooBackupStore
	snapshot string
	// host and tags pick the newest snapshot when snapshot is "latest".
	host string
	tags []string
	// repository is the restic repository folder in store; empty is the
	// instance's own.
	repository string
	// file is an Odoo backup file in store, restored instead of a snapshot.
	file string
	// undo is the restore being undone.
	undo *odoov1.OdooRestore
}

// resolveRestore checks a restore against the instance and finds its source.
// A returned refusal is the user's to fix (the restore fails); an error is
// retried.
func (r *OdooDeploymentReconciler) resolveRestore(ctx context.Context, od *odoov1.OdooDeployment, w instanceWork,
	rs *odoov1.OdooRestore) (src restoreSource, refusal string, err error) {
	spec := rs.Spec
	switch {
	case spec.Backup != "":
		b := &odoov1.OdooBackup{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: od.Namespace, Name: spec.Backup}, b); err != nil {
			if apierrors.IsNotFound(err) {
				return src, fmt.Sprintf("OdooBackup %q does not exist", spec.Backup), nil
			}
			return src, "", err
		}
		if b.Spec.OdooDeployment != od.Name {
			return src, fmt.Sprintf("OdooBackup %q is a backup of %s, not of %s", b.Name, b.Spec.OdooDeployment, od.Name), nil
		}
		switch b.Status.Phase {
		case odoov1.BackupPhaseCompleted, odoov1.BackupPhaseCompletedWithWarnings, odoov1.BackupPhasePartiallyCompleted:
		default:
			return src, fmt.Sprintf("OdooBackup %q has not completed (phase %q)", b.Name, b.Status.Phase), nil
		}
		return r.backupSource(ctx, od, b, true, func(msg string) {
			r.objectEvent(rs, corev1.EventTypeWarning, ReasonRestoreFromCopy, "Restoring the copy in store "+msg)
		})
	case spec.SnapshotID != "":
		if len(od.Spec.Backup.Stores) == 0 {
			return src, fmt.Sprintf("OdooDeployment %s has no spec.backup.stores to find snapshot %s in", od.Name, spec.SnapshotID), nil
		}
		store, err := r.getStore(ctx, od.Namespace, od.Spec.Backup.Stores[0])
		if err != nil {
			return src, err.Error(), nil
		}
		return restoreSource{store: store, snapshot: spec.SnapshotID}, "", nil
	case spec.From != nil:
		return r.fromSource(ctx, od, spec.From)
	case spec.Undo != "":
		target := w.restore(spec.Undo)
		switch {
		case spec.Undo == rs.Name:
			return src, "a restore cannot undo itself", nil
		case target == nil:
			return src, fmt.Sprintf("OdooRestore %q of %s does not exist", spec.Undo, od.Name), nil
		case target.Status.Phase != odoov1.RestorePhaseCompleted:
			return src, fmt.Sprintf("OdooRestore %q has not completed", spec.Undo), nil
		case target.Status.Previous.State != odoov1.PreviousKept:
			return src, fmt.Sprintf("the data OdooRestore %q replaced is no longer kept (%s)", spec.Undo,
				strings.ToLower(orDefault(target.Status.Previous.State, "none"))), nil
		}
		for i := range w.restores {
			other := &w.restores[i]
			if other.Name == target.Name || other.Status.Phase != odoov1.RestorePhaseCompleted || other.Status.CompletedAt == nil {
				continue
			}
			if target.Status.CompletedAt == nil || other.Status.CompletedAt.After(target.Status.CompletedAt.Time) {
				return src, fmt.Sprintf("OdooRestore %q ran after %q; only the latest restore can be undone", other.Name, target.Name), nil
			}
		}
		return restoreSource{undo: target}, "", nil
	}
	return src, "set one of backup, snapshotID, from, undo", nil
}

// fromSource finds the source of a restore from spec.from: a file or a
// restic repository in any store, written by anyone.
func (r *OdooDeploymentReconciler) fromSource(ctx context.Context, od *odoov1.OdooDeployment,
	from *odoov1.BackupSource) (src restoreSource, refusal string, err error) {
	store, err := r.getStore(ctx, od.Namespace, from.Store)
	if err != nil {
		return src, err.Error(), nil
	}
	if from.IsFile() {
		return restoreSource{store: store, file: from.Path}, "", nil
	}
	if store.Spec.EncryptionKeySecret == nil {
		return src, fmt.Sprintf("OdooBackupStore %s has no encryptionKeySecret, so it can only hold backup files "+
			"(a path ending in .zip or .dump), not the restic repository %s", store.Name, from.Path), nil
	}
	return restoreSource{store: store, repository: from.Path, snapshot: from.SnapshotValue(),
		host: from.Host, tags: from.Tags}, "", nil
}

// backupSource finds where a finished backup can be read from: the store it
// was written to, or, when that store is gone or not ready, a copy in a
// store that is (fromCopy is told which and why); a zip copy only when
// zips is set.
func (r *OdooDeploymentReconciler) backupSource(ctx context.Context, od *odoov1.OdooDeployment, b *odoov1.OdooBackup,
	zips bool, fromCopy func(msg string)) (src restoreSource, refusal string, err error) {
	if b.Status.SnapshotID == "" || b.Status.Store == "" {
		return src, fmt.Sprintf("OdooBackup %q records no snapshot", b.Name), nil
	}
	store, err := r.getStore(ctx, od.Namespace, b.Status.Store)
	if err == nil && store.Status.Ready {
		return restoreSource{store: store, snapshot: b.Status.SnapshotID}, "", nil
	}
	for _, c := range b.Status.Copies {
		if c.SnapshotID == "" && (c.File == "" || !zips) {
			continue
		}
		if copyStore, copyErr := r.getStore(ctx, od.Namespace, c.Store); copyErr == nil && copyStore.Status.Ready {
			why := "is not ready"
			if err != nil {
				why = "cannot be used: " + err.Error()
			}
			fromCopy(fmt.Sprintf("%s: store %s %s", c.Store, b.Status.Store, why))
			if c.SnapshotID == "" {
				return restoreSource{store: copyStore, file: c.File}, "", nil
			}
			return restoreSource{store: copyStore, snapshot: c.SnapshotID}, "", nil
		}
	}
	if err != nil {
		return src, err.Error(), nil
	}
	return restoreSource{store: store, snapshot: b.Status.SnapshotID}, "", nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func restoreMode(rs *odoov1.OdooRestore) string {
	if rs.Spec.Undo != "" {
		return metrics.RestoreModeUndo
	}
	return metrics.RestoreModeRestore
}

// startRestore claims the instance for restore rs.
func (r *OdooDeploymentReconciler) startRestore(ctx context.Context, s *reconcileState, w instanceWork,
	rs *odoov1.OdooRestore, configHash string) (ctrl.Result, error) {
	od := s.od
	if !rs.DeletionTimestamp.IsZero() && (rs.Status.Phase == "" || rs.Status.Phase == odoov1.RestorePhasePending) {
		return ctrl.Result{RequeueAfter: time.Second}, r.setRestoreFinalizer(ctx, rs, false)
	}
	resumed := rs.Status.Phase == odoov1.RestorePhaseStopping || rs.Status.Phase == odoov1.RestorePhaseRestoring
	if !resumed {
		_, refusal, err := r.resolveRestore(ctx, od, w, rs)
		if err != nil {
			return ctrl.Result{}, err
		}
		if refusal != "" {
			r.objectEvent(rs, corev1.EventTypeWarning, ReasonRestoreRefused, refusal)
			metrics.RestoreFinished(restoreMode(rs), metrics.RestoreFailed)
			return ctrl.Result{RequeueAfter: time.Second}, r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) {
				st.Phase = odoov1.RestorePhaseFailed
				st.Message = refusal
				now := metav1.Now()
				st.CompletedAt = &now
				st.CleanedUp = true // nothing ran, nothing to clean
			})
		}
		now := metav1.Now()
		for i := range w.restores {
			if w.restores[i].Status.Suffix == layout.Suffix(now.Time) {
				// Suffixes have a resolution of one second and name data.
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
		}
		if err := r.setRestoreFinalizer(ctx, rs, true); err != nil {
			return ctrl.Result{}, err
		}
		err = r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) {
			st.Phase = odoov1.RestorePhaseStopping
			st.Message = "stopping Odoo"
			st.Suffix = layout.Suffix(now.Time)
			st.StartedAt = &now
			st.Previous = odoov1.RestorePrevious{Image: od.Status.AppliedImage, UpgradeToken: od.Status.AppliedUpgradeToken}
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		r.objectEvent(rs, corev1.EventTypeNormal, ReasonRestoreRunning, "stopping Odoo to restore")
	}
	now := metav1.Now()
	od.Status.CurrentOperation = odoov1.OperationStatus{
		Kind: odoov1.OperationRestore, Name: rs.Name, StoppedOdoo: true, StartedAt: &now, Job: rs.Status.Job,
	}
	log.FromContext(ctx).Info("Starting restore", "restore", rs.Name, "resumed", resumed)
	result, _, err := r.driveRestore(ctx, s, w, rs, configHash)
	return result, err
}

// restoreRetryDelay is the backoff after a failed attempt of a restore that
// has to be finished.
func restoreRetryDelay(attempts int32) time.Duration {
	d := 30 * time.Second
	for i := int32(1); i < attempts && d < maxRestoreRetryDelay; i++ {
		d *= 2
	}
	if d > maxRestoreRetryDelay {
		d = maxRestoreRetryDelay
	}
	return d
}

// driveRestore moves the restore in status.currentOperation along.
//
//nolint:gocyclo // one switch over the restore stages
func (r *OdooDeploymentReconciler) driveRestore(ctx context.Context, s *reconcileState, w instanceWork,
	rs *odoov1.OdooRestore, configHash string) (ctrl.Result, bool, error) {
	od := s.od
	op := &od.Status.CurrentOperation
	od.Status.Phase = odoov1.PhaseRestoring
	s.ready(metav1.ConditionFalse, ReasonRestoreRunning, "Odoo is stopped while OdooRestore "+rs.Name+" runs")

	if op.Job == "" && !rs.DeletionTimestamp.IsZero() && !rs.Status.LiveChanged && rs.Status.Phase != odoov1.RestorePhaseRestoring {
		// Deleted while Odoo was being stopped: no Job ran, nothing to undo.
		if _, _, err := r.finishRestoreFailed(ctx, s, rs, "the OdooRestore was deleted before it ran"); err != nil {
			return ctrl.Result{}, false, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, true, r.setRestoreFinalizer(ctx, rs, false)
	}
	if op.Job == "" {
		if rs.Status.RetryAfter != nil && rs.Status.RetryAfter.After(time.Now()) {
			return ctrl.Result{RequeueAfter: time.Until(rs.Status.RetryAfter.Time)}, false, nil
		}
		stopped, err := r.stopOdoo(ctx, s, configHash)
		if err != nil {
			res, err := s.fail(odoov1.ReasonDeploymentFailed, err)
			return res, false, err
		}
		if !stopped {
			s.ready(metav1.ConditionFalse, odoov1.ReasonWaitingForPodsToStop, "stopping Odoo for OdooRestore "+rs.Name)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, false, nil
		}
		// A swap that started is finished from the restore journal on the
		// data volume alone, so its retries never look the source up again:
		// the OdooBackup, the store or its Secret may be gone or changed by
		// then. A change only an attempt without a result suggested is
		// retried in full while the source can still be had (the journal
		// may show the swap never started); finishing it otherwise either
		// finishes the swap or reports that it never started.
		var src restoreSource
		finishing := rs.Status.LiveChanged && !rs.Status.LiveChangeUnconfirmed
		var noSource string
		if !finishing {
			var refusal string
			src, refusal, err = r.resolveRestore(ctx, od, w, rs)
			if err != nil {
				return ctrl.Result{}, false, err
			}
			switch {
			case refusal != "" && !rs.Status.LiveChanged:
				return r.finishRestoreFailed(ctx, s, rs, refusal)
			case refusal != "":
				finishing, noSource = true, refusal
			}
		}
		params, err := r.jobParams(ctx, od, src.store)
		if err != nil {
			s.degraded(ReasonBackupImageUnknown, err.Error())
			return ctrl.Result{RequeueAfter: time.Minute}, false, nil
		}
		attempt := rs.Status.Attempts + 1
		var job batchv1.Job
		if finishing {
			job = jobspec.FinishRestoreJob(params, rs, rs.Status.Suffix, rs.Spec.Undo != "", attempt)
		} else {
			jobSrc := jobspec.RestoreSource{
				Snapshot: src.snapshot, Host: src.host, Tags: src.tags,
				Repository: src.repository, File: src.file, Neutralize: rs.Spec.Neutralize,
			}
			if src.undo != nil {
				jobSrc.UndoSuffix = src.undo.Status.Suffix
			}
			if rs.Spec.From != nil || rs.Spec.Neutralize {
				// A backup from elsewhere records no image to compare with, so
				// the instance's own Odoo reports the version it will run;
				// it is also the Odoo that neutralizes.
				jobSrc.OdooImage = orDefault(od.Status.AppliedImage, od.Spec.Image)
			}
			if rs.Spec.Upgrade {
				// The data is upgraded to the image the instance is to run,
				// for the upgrade token it has now; the agent reports both
				// back from the attempt that upgraded, so a token changed
				// later is still upgraded for.
				jobSrc.OdooImage = od.Spec.Image
				jobSrc.UpgradeModules = restoreUpgradeModules(od)
				jobSrc.UpgradeToken = od.Spec.Upgrade.Token
			}
			build := r.restoreJob
			if build == nil {
				build = jobspec.RestoreJob
			}
			job, err = build(params, rs, rs.Status.Suffix, jobSrc, od.Spec.Modules, attempt)
			switch {
			case err != nil && !rs.Status.LiveChanged:
				return r.finishRestoreFailed(ctx, s, rs, err.Error())
			case err != nil:
				// The swap may have started, so the restore cannot fail
				// here as one that left the live data alone: the journal
				// tells, through the Job that only finishes the swap.
				finishing, noSource = true, "cannot restore from the source again: "+err.Error()
				job = jobspec.FinishRestoreJob(params, rs, rs.Status.Suffix, rs.Spec.Undo != "", attempt)
			}
		}
		started, err := r.startOperationJob(ctx, s, &job)
		if err != nil {
			res, err := s.fail(odoov1.ReasonJobCreationFailed, err)
			return res, false, err
		}
		if started != job.Name {
			op.Job = started
			return ctrl.Result{RequeueAfter: 10 * time.Second}, false, nil
		}
		err = r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) {
			st.Phase = odoov1.RestorePhaseRestoring
			st.Message = "job " + job.Name + " is restoring"
			st.Job = job.Name
			st.Attempts = attempt
			st.RetryAfter = nil
			if finishing {
				// What was restored stays as the first attempt recorded it.
				st.Message = "job " + job.Name + " is finishing the swap from the restore journal, without the source"
				if noSource != "" {
					st.Message += " (" + noSource + ")"
				}
				return
			}
			if src.store != nil {
				st.Store = src.store.Name
			}
			st.SnapshotID = src.snapshot
			st.File = src.file
		})
		if err != nil {
			return ctrl.Result{}, false, err
		}
		op.Job = job.Name
	}

	out, err := r.observeJob(ctx, od.Namespace, op.Job)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	switch out.state {
	case reconcileloops.JobStateMissing:
		op.Job = ""
		return ctrl.Result{RequeueAfter: time.Second}, false, nil
	case reconcileloops.JobStateActive:
		if out.startMessage != "" {
			msg := "job " + op.Job + " cannot start: " + out.startMessage
			_ = r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) { st.Message = msg })
			s.degraded(odoov1.ReasonJobCannotStart, fmt.Sprintf("OdooRestore %s: %s; Odoo is stopped meanwhile, "+
				"and a Job none of whose pods starts a container within %s is given up", rs.Name, msg, jobStartLimit))
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, false, nil
	}
	if out.state == reconcileloops.JobStateSucceeded && out.decoded && out.result.OK {
		return r.finishRestore(ctx, s, w, rs, out.result)
	}

	msg := fmt.Sprintf("job %s %s; inspect with: kubectl -n %s logs job/%s", op.Job, failureText(out), od.Namespace, op.Job)
	liveChanged, unconfirmed := liveChangeAfter(rs.Status, out)
	if !liveChanged {
		if !out.decoded {
			msg += "; no pod of it got past the steps before the swap"
		}
		if rs.Status.LiveChanged {
			// Only an earlier attempt without a result made it look started.
			msg += "; the restore journal shows the swap never started"
			if err := r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) {
				st.LiveChanged = false
				st.LiveChangeUnconfirmed = false
			}); err != nil {
				return ctrl.Result{}, false, err
			}
		}
		return r.finishRestoreFailed(ctx, s, rs, msg)
	}
	// The swap may have started: the only safe way out is forward.
	delay := restoreRetryDelay(rs.Status.Attempts)
	retry := metav1.NewTime(time.Now().Add(delay))
	full := fmt.Sprintf("%s; the restore may have started replacing the live data, so Odoo stays stopped and it is retried at %s",
		msg, retry.UTC().Format(time.RFC3339))
	err = r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) {
		st.Message = full
		st.LiveChanged = true
		st.LiveChangeUnconfirmed = unconfirmed
		st.RetryAfter = &retry
		st.Job = ""
	})
	if err != nil {
		return ctrl.Result{}, false, err
	}
	s.degraded(ReasonRestoreIncomplete, full)
	r.objectEvent(rs, corev1.EventTypeWarning, ReasonRestoreIncomplete, full)
	op.Job = ""
	return ctrl.Result{RequeueAfter: delay}, false, nil
}

// liveChangeAfter decides, after a failed restore attempt, whether the
// restore may have changed the live data, which only its main container
// does (the swap), and whether that rests only on attempts that left no
// result. The agent's result is read from the restore journal on the data
// volume, so it speaks for every attempt before it: a result saying the swap
// has not started clears a change that only an attempt without a result
// suggested, never one a result reported. An attempt whose pods never
// reached the main container changes nothing either way.
func liveChangeAfter(st odoov1.OdooRestoreStatus, out jobOutcome) (changed, unconfirmed bool) {
	confirmed := st.LiveChanged && !st.LiveChangeUnconfirmed
	switch {
	case out.decoded && out.result.LiveChanged:
		return true, false
	case out.decoded:
		return confirmed, false
	case out.mainStarted:
		return true, !confirmed
	default:
		return st.LiveChanged, st.LiveChangeUnconfirmed
	}
}

// restoreUpgradeModules are the modules an OdooRestore with spec.upgrade
// upgrades: spec.upgrade.modules, or all of them when that is empty.
func restoreUpgradeModules(od *odoov1.OdooDeployment) []string {
	if len(od.Spec.Upgrade.Modules) == 0 {
		return []string{"all"}
	}
	return od.Spec.Upgrade.Modules
}

// finishRestore records a restore that swapped the data in and points the
// instance at what the backup ran.
func (r *OdooDeploymentReconciler) finishRestore(ctx context.Context, s *reconcileState, w instanceWork,
	rs *odoov1.OdooRestore, res result.Result) (ctrl.Result, bool, error) {
	od := s.od
	image, token := res.Image, res.UpgradeToken
	if rs.Spec.Undo != "" {
		if target := w.restore(rs.Spec.Undo); target != nil {
			image, token = target.Status.Previous.Image, target.Status.Previous.UpgradeToken
			err := r.patchRestoreStatus(ctx, target, func(st *odoov1.OdooRestoreStatus) {
				st.Previous.State = odoov1.PreviousUndone
				st.Previous.UndoneBy = rs.Name
				st.Previous.Database, st.Previous.Filestore = "", ""
				st.Previous.KeptUntil = nil
			})
			if err != nil {
				return ctrl.Result{}, false, err
			}
		}
	}
	// An upgraded restore counts as upgraded for the token its Job upgraded
	// the data for (res.UpgradeToken), not the one spec.upgrade.token has
	// now: a token changed while it ran still gets its upgrade.
	if image == "" && rs.Spec.From != nil && !rs.Spec.Upgrade {
		// A backup from elsewhere records neither: the data now runs on
		// this instance's image and counts as upgraded for its token.
		token = od.Spec.Upgrade.Token
	}
	keep := od.Spec.Backup.KeepPreviousValue()
	if rs.Spec.KeepPrevious != nil {
		keep = rs.Spec.KeepPrevious.Duration
	}
	db := od.Status.Database.Name
	now := metav1.Now()
	msg := "the data was restored"
	if res.PreviousKept {
		msg += fmt.Sprintf("; the replaced data is kept as %s until %s", layout.PrevName(db, rs.Status.Suffix),
			now.Add(keep).UTC().Format(time.RFC3339))
	}
	if len(res.MissingModules) > 0 {
		msg += fmt.Sprintf("; modules %s are installed again", strings.Join(res.MissingModules, ", "))
	}
	if rs.Spec.Upgrade && rs.Spec.Undo == "" && token != od.Spec.Upgrade.Token {
		msg += fmt.Sprintf("; it was upgraded for upgrade token %q and spec.upgrade.token is now %q, "+
			"which is applied next as any change of the token is", token, od.Spec.Upgrade.Token)
	}
	err := r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) {
		st.Phase = odoov1.RestorePhaseCompleted
		st.Message = msg
		st.CompletedAt = &now
		st.Image = image
		st.UpgradeToken = token
		st.OdooVersion = res.OdooVersion
		st.LiveChanged = true
		st.LiveChangeUnconfirmed = false
		st.RetryAfter = nil
		st.Warnings = res.Warnings
		if res.SnapshotID != "" {
			st.SnapshotID = res.SnapshotID
		}
		if res.File != "" {
			st.File = res.File
		}
		if res.PreviousKept {
			until := metav1.NewTime(now.Add(keep))
			st.Previous.State = odoov1.PreviousKept
			st.Previous.Database = layout.PrevName(db, st.Suffix)
			st.Previous.Filestore = layout.PrevName(db, st.Suffix)
			st.Previous.KeptUntil = &until
		} else {
			st.Previous.State = odoov1.PreviousNone
		}
	})
	if err != nil {
		return ctrl.Result{}, false, err
	}
	if image != "" {
		od.Status.AppliedImage = image
	}
	od.Status.AppliedUpgradeToken = token
	installed := utils.Difference(od.Spec.Modules, res.MissingModules)
	if len(installed) == 0 {
		// The data is an initialized Odoo database whatever spec.modules
		// lists, so the next reconcile must not take it for a first boot.
		installed = []string{"base"}
	}
	od.Status.InitModulesInstalled = installed
	if rs.Name == od.Status.Bootstrap.Restore {
		od.Status.Bootstrap.Completed = true
	}
	od.Status.CurrentOperation = odoov1.OperationStatus{}
	r.objectEvent(rs, corev1.EventTypeNormal, ReasonRestoreCompleted, msg)
	r.event(od, corev1.EventTypeNormal, ReasonRestoreCompleted, "OdooRestore "+rs.Name+": "+msg)
	metrics.RestoreFinished(restoreMode(rs), metrics.RestoreSucceeded)
	s.healthy()
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// finishRestoreFailed records a restore that failed before touching live data.
func (r *OdooDeploymentReconciler) finishRestoreFailed(ctx context.Context, s *reconcileState, rs *odoov1.OdooRestore, msg string) (ctrl.Result, bool, error) {
	msg += "; the live data was not changed"
	err := r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) {
		st.Phase = odoov1.RestorePhaseFailed
		st.Message = msg
		now := metav1.Now()
		st.CompletedAt = &now
		st.RetryAfter = nil
	})
	if err != nil {
		return ctrl.Result{}, false, err
	}
	r.objectEvent(rs, corev1.EventTypeWarning, ReasonRestoreFailed, msg)
	r.event(s.od, corev1.EventTypeWarning, ReasonRestoreFailed, "OdooRestore "+rs.Name+": "+msg)
	metrics.RestoreFinished(restoreMode(rs), metrics.RestoreFailed)
	s.od.Status.CurrentOperation = odoov1.OperationStatus{}
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// ---- cleanups --------------------------------------------------------------------

// startCleanup claims the instance to delete what restore rs left behind.
func (r *OdooDeploymentReconciler) startCleanup(ctx context.Context, s *reconcileState, rs *odoov1.OdooRestore, configHash string) (ctrl.Result, error) {
	_, rwop, err := r.pvcAccess(ctx, s.od)
	if err != nil {
		return s.fail(odoov1.ReasonReconcileFailed, err)
	}
	now := metav1.Now()
	s.od.Status.CurrentOperation = odoov1.OperationStatus{
		Kind: odoov1.OperationCleanup, Name: rs.Name, StoppedOdoo: rwop, StartedAt: &now,
	}
	result, _, err := r.driveCleanup(ctx, s, rs, configHash)
	return result, err
}

// driveCleanup moves the cleanup in status.currentOperation along.
func (r *OdooDeploymentReconciler) driveCleanup(ctx context.Context, s *reconcileState, rs *odoov1.OdooRestore, configHash string) (ctrl.Result, bool, error) {
	od := s.od
	op := &od.Status.CurrentOperation
	if op.Job == "" {
		if op.StoppedOdoo {
			stopped, err := r.stopOdoo(ctx, s, configHash)
			if err != nil {
				res, err := s.fail(odoov1.ReasonDeploymentFailed, err)
				return res, false, err
			}
			if !stopped {
				return ctrl.Result{RequeueAfter: 10 * time.Second}, false, nil
			}
		}
		params, err := r.jobParams(ctx, od, nil)
		if err != nil {
			s.degraded(ReasonBackupImageUnknown, err.Error())
			return ctrl.Result{RequeueAfter: time.Minute}, false, nil
		}
		if !op.StoppedOdoo {
			if params.Affinity, err = r.affinityToOdoo(ctx, od); err != nil {
				res, err := s.fail(odoov1.ReasonReconcileFailed, err)
				return res, false, err
			}
		}
		attempt := rs.Status.CleanupAttempts + 1
		job := jobspec.CleanupJob(params, rs, rs.Status.Suffix, attempt)
		started, err := r.startOperationJob(ctx, s, &job)
		if err != nil {
			res, err := s.fail(odoov1.ReasonJobCreationFailed, err)
			return res, false, err
		}
		if started != job.Name {
			op.Job = started
			return ctrl.Result{RequeueAfter: 10 * time.Second}, false, nil
		}
		if err := r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) { st.CleanupAttempts = attempt }); err != nil {
			return ctrl.Result{}, false, err
		}
		op.Job = job.Name
	}
	out, err := r.observeJob(ctx, od.Namespace, op.Job)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	switch out.state {
	case reconcileloops.JobStateMissing:
		op.Job = ""
		return ctrl.Result{RequeueAfter: time.Second}, false, nil
	case reconcileloops.JobStateActive:
		if out.startMessage != "" {
			s.degraded(odoov1.ReasonJobCannotStart, fmt.Sprintf("cleanup job %s of OdooRestore %s cannot start: %s",
				op.Job, rs.Name, out.startMessage))
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, false, nil
	}
	od.Status.CurrentOperation = odoov1.OperationStatus{}
	if out.state == reconcileloops.JobStateSucceeded && out.decoded && out.result.OK {
		err := r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) {
			st.CleanedUp = true
			st.RetryAfter = nil
			if st.Previous.State == odoov1.PreviousKept {
				st.Previous.State = odoov1.PreviousRemoved
				st.Previous.KeptUntil = nil
			}
		})
		if err != nil {
			return ctrl.Result{}, false, err
		}
		if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, out.job); err != nil {
			return ctrl.Result{}, false, err
		}
		if err := r.setRestoreFinalizer(ctx, rs, false); err != nil {
			return ctrl.Result{}, false, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	msg := fmt.Sprintf("cleanup job %s %s; retried in an hour", op.Job, failureText(out))
	retry := metav1.NewTime(time.Now().Add(time.Hour))
	if err := r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) { st.RetryAfter = &retry }); err != nil {
		return ctrl.Result{}, false, err
	}
	r.objectEvent(rs, corev1.EventTypeWarning, ReasonCleanupFailed, msg)
	r.event(od, corev1.EventTypeWarning, ReasonCleanupFailed, "OdooRestore "+rs.Name+": "+msg)
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// releaseFinishedRestores drops the finalizer of restores that are being
// deleted and have nothing left to clean.
func (r *OdooDeploymentReconciler) releaseFinishedRestores(ctx context.Context, w instanceWork) error {
	for i := range w.restores {
		rs := &w.restores[i]
		if rs.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(rs, odoov1.RestoreFinalizer) {
			continue
		}
		switch rs.Status.Phase {
		case "", odoov1.RestorePhasePending:
		case odoov1.RestorePhaseCompleted, odoov1.RestorePhaseFailed:
			if !rs.Status.CleanedUp && rs.Status.Suffix != "" && (rs.Status.Phase != odoov1.RestorePhaseFailed || !rs.Status.LiveChanged) {
				continue // the cleanup releases it
			}
		default:
			continue // running
		}
		if err := r.setRestoreFinalizer(ctx, rs, false); err != nil {
			return err
		}
	}
	return nil
}

// ---- formatting --------------------------------------------------------------------

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func bytesText(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
