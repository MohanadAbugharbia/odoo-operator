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
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/jobspec"
	"github.com/MohanadAbugharbia/odoo-operator/internal/controller/reconcileloops"
	"github.com/MohanadAbugharbia/odoo-operator/internal/database"
	"github.com/MohanadAbugharbia/odoo-operator/internal/metrics"
	"github.com/MohanadAbugharbia/odoo-operator/pkg/utils"
)

// OdooDeploymentReconciler reconciles a OdooDeployment object
type OdooDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// APIReader is an uncached reader for one-off lookups (pods, events).
	// Falls back to Client when nil.
	APIReader client.Reader
	// DB provisions and drops the Odoo database.
	DB database.Provisioner
	// Recorder emits Kubernetes events on the OdooDeployment.
	Recorder record.EventRecorder
	// MaintenancePageImage is the image that serves spec.maintenancePage:
	// the operator's own. When empty it is looked up with OwnImage.
	MaintenancePageImage string
	// OwnImage returns the operator's own image. It is tried on every
	// reconcile that needs the page until it succeeds, so a lookup that fails
	// once (a slow API server at startup) does not disable the page for the
	// operator's lifetime. Nil, with no MaintenancePageImage, disables the page.
	OwnImage func(context.Context) (string, error)
	// BackupImage runs the backup, restore and cleanup Jobs. When empty it
	// is derived from the operator's own image (BackupImageFor).
	BackupImage string
	// Version is the operator's version, recorded in every backup.
	Version string
	// Now is the clock of backup schedules; time.Now when nil.
	Now func() time.Time

	// restoreJob renders a restore Job; jobspec.RestoreJob when nil. Tests
	// set it to make the rendering fail.
	restoreJob func(p jobspec.Params, restore *odoov1.OdooRestore, suffix string, src jobspec.RestoreSource,
		expectModules []string, attempt int32) (batchv1.Job, error)

	imageMu sync.Mutex
}

var apiSGVString = odoov1.GroupVersion.String()

// IsOwnedByOdooDeployment checks that an object is owned by a OdooDeployment and returns
// the owner name
func IsOwnedByOdooDeployment(obj client.Object) (string, bool) {
	owner := metav1.GetControllerOf(obj)
	if owner == nil {
		return "", false
	}

	if owner.Kind != odoov1.OdooDeploymentKind {
		return "", false
	}

	if owner.APIVersion != apiSGVString {
		return "", false
	}

	return owner.Name, true
}

// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoodeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoodeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoodeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// reconcileState carries one reconcile's working copy of the OdooDeployment
// and the snapshot the deferred status patch is computed against. start is
// the status as this reconcile found it (base is re-baselined by mid-reconcile
// patches); metrics compare against it so an event is counted once, on the
// reconcile that causes it, not again on every requeue.
type reconcileState struct {
	r     *OdooDeploymentReconciler
	od    *odoov1.OdooDeployment
	base  *odoov1.OdooDeployment
	start odoov1.OdooDeploymentStatus
	// wake is when the reconcile must run again at the latest whatever it
	// returns, such as for the next scheduled backup; zero for no limit.
	wake time.Duration
}

// patchStatus writes status changes made so far as a merge patch on the
// status subresource and re-baselines. It is idempotent and safe to call
// mid-reconcile (used before CREATE DATABASE) as well as deferred.
func (s *reconcileState) patchStatus(ctx context.Context) error {
	s.od.Status.ObservedGeneration = s.od.Generation
	if equality.Semantic.DeepEqual(s.base.Status, s.od.Status) {
		return nil
	}
	if err := s.r.Status().Patch(ctx, s.od, client.MergeFrom(s.base)); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("patch status: %w", err)
	}
	// The response carries the stored spec; keep working on the normalised one.
	s.od.DeduplicateModules()
	s.base = s.od.DeepCopy()
	return nil
}

// setFinalizers adds or removes the database and operations finalizers with
// a metadata-only patch on a copy, so the in-memory status is never
// overwritten by the API-server response.
func (s *reconcileState) setFinalizers(ctx context.Context, database, operations bool) error {
	patched := s.od.DeepCopy()
	changed := false
	for _, f := range []struct {
		name string
		want bool
	}{{odoov1.DatabaseFinalizer, database}, {odoov1.OperationsFinalizer, operations}} {
		if f.want {
			changed = controllerutil.AddFinalizer(patched, f.name) || changed
		} else {
			changed = controllerutil.RemoveFinalizer(patched, f.name) || changed
		}
	}
	if !changed {
		return nil
	}
	if err := s.r.Patch(ctx, patched, client.MergeFrom(s.od)); err != nil {
		return fmt.Errorf("patch finalizers: %w", err)
	}
	s.od.ObjectMeta = patched.ObjectMeta
	s.base.ObjectMeta = *patched.ObjectMeta.DeepCopy()
	log.FromContext(ctx).Info("Updated finalizers", "database", database, "operations", operations)
	return nil
}

// syncFinalizers sets the finalizers the instance needs now.
func (s *reconcileState) syncFinalizers(ctx context.Context) error {
	return s.setFinalizers(ctx, wantsFinalizer(s.od), wantsOperationsFinalizer(s.od))
}

// holdForJob makes sure a Job about to be created cannot be killed by the
// instance's deletion: the database finalizer already waits for its pods,
// otherwise the operations finalizer is added before the Job exists.
func (s *reconcileState) holdForJob(ctx context.Context) error {
	if wantsFinalizer(s.od) || controllerutil.ContainsFinalizer(s.od, odoov1.OperationsFinalizer) {
		return nil
	}
	return s.setFinalizers(ctx, controllerutil.ContainsFinalizer(s.od, odoov1.DatabaseFinalizer), true)
}

// enteredDegraded is true when this reconcile left the OdooDeployment
// Degraded with reason and it was not already Degraded with that reason.
func (s *reconcileState) enteredDegraded(reason string) bool {
	isNow := func(conds []metav1.Condition) bool {
		c := meta.FindStatusCondition(conds, odoov1.ConditionDegraded)
		return c != nil && c.Status == metav1.ConditionTrue && c.Reason == reason
	}
	return isNow(s.od.Status.Conditions) && !isNow(s.start.Conditions)
}

// countDegradedTransitions counts the failures that the reconcile retries
// (and so would otherwise count on every requeue) once, when they start.
func (s *reconcileState) countDegradedTransitions() {
	od := s.od
	if s.enteredDegraded(odoov1.ReasonImagePullFailed) {
		metrics.ImagePullFailed(od.Namespace, od.Name)
	}
	if s.enteredDegraded(odoov1.ReasonDatabaseCreateFailed) {
		metrics.DatabaseOperation(metrics.DatabaseCreate, metrics.DatabaseFailed)
	}
	if s.enteredDegraded(odoov1.ReasonDatabaseDropFailed) {
		metrics.DatabaseOperation(metrics.DatabaseDrop, metrics.DatabaseFailed)
	}
}

func (s *reconcileState) condition(conditionType string, status metav1.ConditionStatus, reason, message string) {
	utils.SetCondition(&s.od.Status.Conditions, conditionType, status, reason, message, s.od.Generation)
}

func (s *reconcileState) degraded(reason, message string) {
	s.condition(odoov1.ConditionDegraded, metav1.ConditionTrue, reason, message)
}

func (s *reconcileState) healthy() {
	s.condition(odoov1.ConditionDegraded, metav1.ConditionFalse, odoov1.ReasonReconcileSucceeded, "")
}

func (s *reconcileState) ready(status metav1.ConditionStatus, reason, message string) {
	s.condition(odoov1.ConditionReady, status, reason, message)
}

func (s *reconcileState) initialized() {
	if len(s.od.Status.InitModulesInstalled) > 0 {
		s.condition(odoov1.ConditionInitialized, metav1.ConditionTrue, odoov1.ReasonInitJobSucceeded,
			fmt.Sprintf("%d modules installed", len(s.od.Status.InitModulesInstalled)))
	} else {
		s.condition(odoov1.ConditionInitialized, metav1.ConditionFalse, odoov1.ReasonNotInitialized, "the init Job has not succeeded yet")
	}
}

// fail records a Degraded condition and returns the error so that
// controller-runtime retries with backoff.
func (s *reconcileState) fail(reason string, err error) (ctrl.Result, error) {
	s.degraded(reason, err.Error())
	return ctrl.Result{}, err
}

func (r *OdooDeploymentReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *OdooDeploymentReconciler) event(od *odoov1.OdooDeployment, eventType, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Event(od, eventType, reason, message)
	}
}

// countPods returns the number of pods matching labels that may still run:
// terminating ones count, finished ones (Succeeded or Failed) do not. A
// finished Job keeps its pods until the Job goes, which for a restore Job is
// its TTL; they hold no connection and must not stall a delete.
func (r *OdooDeploymentReconciler) countPods(ctx context.Context, namespace string, labels map[string]string) (int, error) {
	pods := &corev1.PodList{}
	if err := r.reader().List(ctx, pods, client.InNamespace(namespace), client.MatchingLabels(labels)); err != nil {
		return 0, fmt.Errorf("list pods: %w", err)
	}
	n := 0
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			n++
		}
	}
	return n, nil
}

// Reconcile drives an OdooDeployment through Pending → Initializing →
// Running, runs upgrade Jobs on image/token changes and drops the database
// on deletion when the operator created it.
func (r *OdooDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := log.FromContext(ctx)

	// Read from the API server: a cached copy can lag this reconciler's own
	// last status write, and status.currentOperation must never be stale.
	od := &odoov1.OdooDeployment{}
	if err := r.reader().Get(ctx, req.NamespacedName, od); err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("OdooDeployment resource object not found.")
			metrics.Forget(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get OdooDeployment: %w", err)
	}
	logger.V(1).Info("Reconciling OdooDeployment", "resourceVersion", od.ResourceVersion, "generation", od.Generation)

	od.DeduplicateModules()
	s := &reconcileState{r: r, od: od, base: od.DeepCopy(), start: *od.Status.DeepCopy()}
	defer func() {
		if patchErr := s.patchStatus(ctx); patchErr != nil {
			err = utilerrors.NewAggregate([]error{err, patchErr})
		} else if s.od.DeletionTimestamp.IsZero() && !wantsOperationsFinalizer(s.od) &&
			controllerutil.ContainsFinalizer(s.od, odoov1.OperationsFinalizer) {
			// The Job this reconcile saw finish no longer holds the
			// instance; its status is written, so a later reconcile cannot
			// need the finalizer back for it.
			if finErr := s.setFinalizers(ctx, controllerutil.ContainsFinalizer(s.od, odoov1.DatabaseFinalizer), false); finErr != nil {
				err = utilerrors.NewAggregate([]error{err, finErr})
			}
		}
		// After the status patch, so the page shows what this reconcile decided.
		if pubErr := r.publishMaintenancePage(ctx, s.od); pubErr != nil {
			metrics.MaintenancePagePublishFailed(s.od.Namespace, s.od.Name)
			err = utilerrors.NewAggregate([]error{err, pubErr})
		}
		s.countDegradedTransitions()
		if err == nil && s.wake > 0 && (result.RequeueAfter == 0 || s.wake < result.RequeueAfter) {
			result.RequeueAfter = s.wake
		}
	}()

	if !od.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, s)
	}
	return r.reconcileNormal(ctx, s)
}

//nolint:gocyclo // the lifecycle state machine
func (r *OdooDeploymentReconciler) reconcileNormal(ctx context.Context, s *reconcileState) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	od := s.od
	metrics.Track(od.Namespace, od.Name)

	// The database finalizer is only needed while the operator may own the
	// database: deletionPolicy Delete and provisionedBy not yet known or
	// "operator". Otherwise the operations finalizer holds the instance while
	// one of its Jobs runs.
	if err := s.syncFinalizers(ctx); err != nil {
		return ctrl.Result{}, err
	}

	if bad := od.Spec.Config.InvalidExtraOptionKeys(); len(bad) > 0 {
		msg := fmt.Sprintf("spec.config.extraOptions keys must match ^[a-z_][a-z0-9_]*$: %v", bad)
		s.degraded(odoov1.ReasonInvalidSpec, msg)
		s.ready(metav1.ConditionFalse, odoov1.ReasonInvalidSpec, msg)
		od.Status.Phase = odoov1.PhasePending
		return ctrl.Result{}, nil
	}

	conn, err := od.Spec.Database.GetDbConnectionDetails(r.Client, ctx, od.Namespace)
	if err != nil {
		msg := fmt.Sprintf("could not resolve the database connection: %v", err)
		s.degraded(odoov1.ReasonDatabaseConnectionFailed, msg)
		s.condition(odoov1.ConditionDatabaseReady, metav1.ConditionFalse, odoov1.ReasonDatabaseConnectionFailed, msg)
		s.ready(metav1.ConditionFalse, odoov1.ReasonDatabaseConnectionFailed, msg)
		od.Status.Phase = odoov1.PhasePending
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	adminSecret, err := reconcileloops.EnsureAdminPasswordSecret(ctx, r.Client, r.Scheme, od)
	if err != nil {
		return s.fail(odoov1.ReasonOdooAdminSecretCreationFailed, err)
	}
	od.Status.OdooAdminSecretName = adminSecret.Name

	serializedConfig := od.Spec.Config.GetSerializedOdooConfig(string(adminSecret.Data["password"]), conn)
	configSecret, configHash, err := reconcileloops.EnsureConfigSecret(ctx, r.Client, r.Scheme, od, serializedConfig)
	if err != nil {
		return s.fail(odoov1.ReasonOdooConfigSecretCreationFailed, err)
	}
	od.Status.OdooConfigSecretName = configSecret.Name

	pvc, err := reconcileloops.EnsureFilestorePVC(ctx, r.Client, r.Scheme, od)
	if err != nil {
		return s.fail(odoov1.ReasonPvcCreationFailed, err)
	}
	od.Status.OdooDataPvcName = pvc.Name

	// The Services only select pods, so they exist from the first reconcile:
	// whatever routes to them (an Ingress) can be created alongside the CR
	// instead of waiting for the init Job, and simply has no endpoints until
	// the Deployment is up.
	if reason, err := r.ensureServices(ctx, od); err != nil {
		return s.fail(reason, err)
	}

	// Database.
	if result, ready, err := r.settleDatabase(ctx, s, conn); err != nil || !ready {
		return result, err
	}

	// Backups and restores of this instance.
	work, err := r.listWork(ctx, od)
	if err != nil {
		return s.fail(odoov1.ReasonReconcileFailed, err)
	}
	if err := r.releaseFinishedRestores(ctx, work); err != nil {
		return s.fail(odoov1.ReasonReconcileFailed, err)
	}

	// Scheduled backups start once the instance is initialized, and are
	// created even while another operation runs: they wait in the queue.
	if len(od.Status.InitModulesInstalled) > 0 {
		wait, created, err := r.scheduleBackups(ctx, s, work, r.now())
		if err != nil {
			return s.fail(odoov1.ReasonReconcileFailed, err)
		}
		s.wake = wait
		if created {
			if work, err = r.listWork(ctx, od); err != nil {
				return s.fail(odoov1.ReasonReconcileFailed, err)
			}
		}
	}

	// A backup, restore or cleanup in flight owns the instance until it ends.
	if od.Status.CurrentOperation.Kind != "" {
		result, done, err := r.observeOperation(ctx, s, work, configHash)
		if err != nil || !done {
			return result, err
		}
		if work, err = r.listWork(ctx, od); err != nil {
			return s.fail(odoov1.ReasonReconcileFailed, err)
		}
	}

	// spec.bootstrap.fromBackup: the first database comes from a backup.
	if needsBootstrap(od) {
		result, proceed, err := r.bootstrap(ctx, s, work, configHash)
		if err != nil || !proceed {
			return result, err
		}
	}

	// A restore goes before everything else: it is how a failed upgrade is
	// undone. It waits for a running init or upgrade Job but replaces a
	// failed one.
	if restore := work.nextRestore(); restore != nil {
		if od.Status.CurrentInitJob.Name != "" {
			abandoned, err := r.abandonFailedJob(ctx, s, restore.Name)
			if err != nil {
				return s.fail(odoov1.ReasonReconcileFailed, err)
			}
			if abandoned {
				return r.startRestore(ctx, s, work, restore, configHash)
			}
		} else {
			return r.startRestore(ctx, s, work, restore, configHash)
		}
	}

	// Maintenance Job in flight?
	if od.Status.CurrentInitJob.Name != "" {
		result, done, err := r.observeCurrentJob(ctx, s)
		if err != nil || !done {
			return result, err
		}
	}

	firstBoot := len(od.Status.InitModulesInstalled) == 0
	initModules := utils.Difference(od.Spec.Modules, od.Status.InitModulesInstalled)
	if od.Status.AppliedImage == "" && !firstBoot {
		// A CR reconciled by 0.2.x: adopt the running image without a Job.
		od.Status.AppliedImage = od.Spec.Image
	}
	imageChanged := !firstBoot && od.Spec.Upgrade.OnImageChangeValue() && od.Status.AppliedImage != od.Spec.Image
	tokenChanged := !firstBoot && od.Spec.Upgrade.Token != od.Status.AppliedUpgradeToken
	var upgradeModules []string
	if imageChanged || tokenChanged {
		upgradeModules = od.Spec.Upgrade.Modules
	}
	needJob := len(initModules) > 0 || len(upgradeModules) > 0
	plainRoll := (imageChanged || tokenChanged) && len(upgradeModules) == 0

	s.initialized()

	// A new image is pulled once before the running pods are stopped for it,
	// so an image that does not exist (yet) leaves the old one serving
	// instead of a Deployment at zero and a Job stuck in ImagePullBackOff.
	imageWait, err := r.gateNewImage(ctx, s, (needJob || plainRoll) && !firstBoot)
	if err != nil {
		return s.fail(odoov1.ReasonReconcileFailed, err)
	}
	if imageWait != "" {
		needJob, plainRoll = false, false
	}

	// spec.backup.beforeUpgrade: a backup of the running version first.
	if !imageChanged && !tokenChanged {
		od.Status.PreUpgradeBackup = odoov1.PreUpgradeBackupStatus{}
	}
	if (needJob || plainRoll) && (imageChanged || tokenChanged) {
		result, proceed, err := r.preUpgradeBackup(ctx, s, work, configHash)
		if err != nil || !proceed {
			return result, err
		}
	}

	if needJob {
		return r.startMaintenanceJob(ctx, s, configHash, initModules, upgradeModules, firstBoot)
	}
	// Cleanups and backups wait for init and upgrades, and for a new image
	// to roll out.
	var retryOperations time.Duration
	if !firstBoot && !plainRoll {
		now := time.Now()
		cleanup, cleanupWait := work.cleanupDue(now)
		if cleanup != nil {
			return r.startCleanup(ctx, s, cleanup, configHash)
		}
		backup, backupWait := work.nextBackup(now)
		if backup != nil {
			return r.startBackup(ctx, s, work, backup, configHash)
		}
		if od.Status.Backup.DrillDue != nil {
			return r.startDrill(ctx, s, work)
		}
		retryOperations = minPositive(cleanupWait, backupWait)
	}
	if plainRoll {
		logger.Info("Rolling out new image without an upgrade Job", "from", od.Status.AppliedImage, "to", od.Spec.Image)
		od.Status.AppliedImage = od.Spec.Image
		od.Status.AppliedUpgradeToken = od.Spec.Upgrade.Token
	}

	result, err := r.reconcileSteadyState(ctx, s, configHash, imageWait)
	if retryOperations > 0 && (result.RequeueAfter == 0 || retryOperations < result.RequeueAfter) {
		result.RequeueAfter = retryOperations
	}
	return result, err
}

func (r *OdooDeploymentReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// minPositive is the smaller of two durations, ignoring zeros.
func minPositive(a, b time.Duration) time.Duration {
	switch {
	case a == 0:
		return b
	case b == 0 || a < b:
		return a
	}
	return b
}

// abandonFailedJob deletes a failed init or upgrade Job so that a restore
// can run instead, and reports whether the instance is free. A Job that is
// still running is waited for.
func (r *OdooDeploymentReconciler) abandonFailedJob(ctx context.Context, s *reconcileState, restore string) (bool, error) {
	od := s.od
	obs, err := reconcileloops.ObserveMaintenanceJob(ctx, r.Client, r.reader(), od)
	if err != nil {
		return false, err
	}
	switch obs.State {
	case reconcileloops.JobStateFailed:
		if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, obs.Job); err != nil {
			return false, err
		}
	case reconcileloops.JobStateMissing:
	default:
		return false, nil
	}
	msg := fmt.Sprintf("OdooRestore %s replaces the failed %s job %s", restore, od.Status.CurrentInitJob.Kind, od.Status.CurrentInitJob.Name)
	log.FromContext(ctx).Info(msg)
	r.event(od, corev1.EventTypeNormal, odoov1.ReasonJobSuperseded, msg)
	od.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{}
	return true, nil
}

// reconcileSteadyState runs status.appliedImage. imageWait is the note from
// gateNewImage when a new spec.image is held back.
func (r *OdooDeploymentReconciler) reconcileSteadyState(ctx context.Context, s *reconcileState, configHash, imageWait string) (ctrl.Result, error) {
	od := s.od
	replicas := od.Spec.ReplicasValue()
	deployment, err := reconcileloops.EnsureDeployment(ctx, r.Client, r.Scheme, od, od.Status.AppliedImage, replicas, configHash)
	if err != nil {
		return s.fail(odoov1.ReasonDeploymentFailed, err)
	}

	od.Status.Phase = odoov1.PhaseRunning
	od.Status.ReadyReplicas = deployment.Status.AvailableReplicas
	if imageWait == "" {
		s.healthy()
	}

	observed := deployment.Status.ObservedGeneration >= deployment.Generation
	switch {
	case replicas == 0:
		s.ready(metav1.ConditionTrue, odoov1.ReasonScaledToZero, "spec.replicas is 0")
	case observed && deployment.Status.AvailableReplicas >= replicas:
		s.ready(metav1.ConditionTrue, odoov1.ReasonDeploymentAvailable,
			fmt.Sprintf("%d/%d replicas available", deployment.Status.AvailableReplicas, replicas))
	default:
		s.ready(metav1.ConditionFalse, odoov1.ReasonDeploymentProgressing,
			fmt.Sprintf("%d/%d replicas available", deployment.Status.AvailableReplicas, replicas))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	if imageWait != "" {
		// Pod status changes are not watched; poll the image check.
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// ensureServices creates or updates the maintenance page and the HTTP and
// poll Services, and returns the failure reason with the error.
func (r *OdooDeploymentReconciler) ensureServices(ctx context.Context, od *odoov1.OdooDeployment) (string, error) {
	maintenance, err := r.ensureMaintenancePage(ctx, od)
	if err != nil {
		return odoov1.ReasonMaintenancePageFailed, err
	}
	if _, err := reconcileloops.EnsureHttpService(ctx, r.Client, r.Scheme, od, maintenance); err != nil {
		return odoov1.ReasonFailedCreateHttpService, err
	}
	if _, err := reconcileloops.EnsurePollService(ctx, r.Client, r.Scheme, od); err != nil {
		return odoov1.ReasonFailedCreatePollService, err
	}
	return "", nil
}

// ensureMaintenancePage runs the maintenance page while Odoo has no available
// replica, and reports whether the http Service should point at it. The poll
// Service never moves: only Odoo answers the longpolling port.
func (r *OdooDeploymentReconciler) ensureMaintenancePage(ctx context.Context, od *odoov1.OdooDeployment) (bool, error) {
	if !od.Spec.MaintenancePage.Enabled {
		return false, reconcileloops.RemoveMaintenancePage(ctx, r.Client, od)
	}
	image, err := r.maintenancePageImage(ctx)
	if err != nil {
		// Leave any page as it is: the next reconcile tries again.
		log.FromContext(ctx).Error(err, "cannot tell the operator's own image; serving no maintenance page yet")
		r.event(od, corev1.EventTypeWarning, odoov1.ReasonMaintenancePageImageUnknown,
			fmt.Sprintf("cannot tell the operator's own image, so no maintenance page is served yet: %v", err))
		return false, nil
	}
	if image == "" {
		log.FromContext(ctx).Info("spec.maintenancePage is enabled but the operator has no maintenance page image; serving no page")
		return false, reconcileloops.RemoveMaintenancePage(ctx, r.Client, od)
	}
	serve := false
	// At spec.replicas 0 nobody is meant to be served, so there is nothing
	// to wait for either.
	if od.Spec.ReplicasValue() > 0 {
		odoo := &appsv1.Deployment{}
		err := r.Get(ctx, client.ObjectKey{Name: od.Name, Namespace: od.Namespace}, odoo)
		switch {
		case apierrors.IsNotFound(err):
			serve = true
		case err != nil:
			return false, fmt.Errorf("get deployment: %w", err)
		default:
			serve = odoo.Status.AvailableReplicas == 0
		}
	}
	replicas := int32(0)
	if serve {
		replicas = 1
	}
	if _, err := reconcileloops.EnsureMaintenancePage(ctx, r.Client, r.Scheme, od, image, replicas); err != nil {
		return false, err
	}
	return serve, nil
}

// maintenancePageImage returns MaintenancePageImage, looking it up with
// OwnImage (and keeping it) while it is empty.
func (r *OdooDeploymentReconciler) maintenancePageImage(ctx context.Context) (string, error) {
	r.imageMu.Lock()
	defer r.imageMu.Unlock()
	if r.MaintenancePageImage != "" || r.OwnImage == nil {
		return r.MaintenancePageImage, nil
	}
	image, err := r.OwnImage(ctx)
	if err != nil {
		return "", err
	}
	r.MaintenancePageImage = image
	log.FromContext(ctx).Info("maintenance page image", "image", image)
	return image, nil
}

// publishMaintenancePage writes the page's status for a page that is enabled.
func (r *OdooDeploymentReconciler) publishMaintenancePage(ctx context.Context, od *odoov1.OdooDeployment) error {
	if !od.Spec.MaintenancePage.Enabled || !od.DeletionTimestamp.IsZero() {
		return nil
	}
	if image, err := r.maintenancePageImage(ctx); err != nil || image == "" {
		return nil
	}
	return reconcileloops.PublishMaintenancePageStatus(ctx, r.Client, r.reader(), r.Scheme, od)
}

// gateNewImage holds a rollout back until spec.image has been pulled once.
// rollout says whether this reconcile would stop the running pods for (or
// roll them to) spec.image. It returns a non-empty note while status.appliedImage
// must keep serving, and prunes image checks that are no longer needed.
func (r *OdooDeploymentReconciler) gateNewImage(ctx context.Context, s *reconcileState, rollout bool) (string, error) {
	od := s.od
	if !rollout || od.Spec.Image == od.Status.AppliedImage {
		return "", reconcileloops.PruneImageCheckJobs(ctx, r.Client, od, "")
	}
	check, err := reconcileloops.CheckImage(ctx, r.Client, r.reader(), r.Scheme, od)
	if err != nil || check.Pulled {
		return "", err
	}
	return r.awaitImage(ctx, s, check), nil
}

// awaitImage records why the reconcile keeps serving status.appliedImage
// while spec.image is being checked, and returns a non-empty note.
func (r *OdooDeploymentReconciler) awaitImage(ctx context.Context, s *reconcileState, check reconcileloops.ImageCheck) string {
	od := s.od
	switch {
	case check.FailureMessage != "":
		msg := fmt.Sprintf("image %s failed its check (%s); still serving %s — inspect with: kubectl -n %s logs job/%s, delete the Job to retry",
			od.Spec.Image, check.FailureMessage, od.Status.AppliedImage, od.Namespace, check.JobName)
		s.degraded(odoov1.ReasonImageCheckFailed, msg)
		r.event(od, corev1.EventTypeWarning, odoov1.ReasonImageCheckFailed, msg)
		return msg
	case check.PullMessage != "":
		msg := fmt.Sprintf("cannot pull image %s (%s); still serving %s until it can be pulled or spec.image changes",
			od.Spec.Image, check.PullMessage, od.Status.AppliedImage)
		s.degraded(odoov1.ReasonImagePullFailed, msg)
		r.event(od, corev1.EventTypeWarning, odoov1.ReasonImagePullFailed, msg)
		return msg
	case check.QuotaMessage != "":
		msg := fmt.Sprintf("cannot start the check of image %s (%s); still serving %s", od.Spec.Image, check.QuotaMessage, od.Status.AppliedImage)
		s.degraded(odoov1.ReasonQuotaExceeded, msg)
		return msg
	default:
		log.FromContext(ctx).Info("Waiting for the image check before rolling out", "image", od.Spec.Image, "job", check.JobName)
		s.healthy()
		return "pulling " + od.Spec.Image
	}
}

// settleDatabase runs the provisioning switch and maps its outcome onto the
// status. ready is false when the reconcile must stop here (with the returned
// result).
func (r *OdooDeploymentReconciler) settleDatabase(
	ctx context.Context,
	s *reconcileState,
	conn odoov1.DatabaseConnectionDetails,
) (ctrl.Result, bool, error) {
	od := s.od
	dbResult, dbErr := reconcileloops.ReconcileDatabase(ctx, r.DB, od, conn, s.patchStatus)
	notReady := func(reason, msg, phase string) {
		s.degraded(reason, msg)
		s.condition(odoov1.ConditionDatabaseReady, metav1.ConditionFalse, reason, msg)
		s.ready(metav1.ConditionFalse, reason, msg)
		od.Status.Phase = phase
	}
	switch {
	case dbErr != nil:
		log.FromContext(ctx).Error(dbErr, "database reconciliation failed", "reason", dbResult.Reason)
		notReady(dbResult.Reason, fmt.Sprintf("%s: %v", dbResult.Reason, dbErr), odoov1.PhasePending)
		return ctrl.Result{RequeueAfter: dbResult.Requeue}, false, nil
	case dbResult.Fatal:
		notReady(dbResult.Reason, dbResult.Message, odoov1.PhaseFailed)
		r.event(od, corev1.EventTypeWarning, dbResult.Reason, dbResult.Message)
		return ctrl.Result{}, false, nil
	case !dbResult.Ready:
		notReady(dbResult.Reason, dbResult.Message, odoov1.PhasePending)
		return ctrl.Result{RequeueAfter: dbResult.Requeue}, false, nil
	}
	if dbResult.Event != "" {
		r.event(od, corev1.EventTypeNormal, dbResult.Event, dbResult.Message)
		switch dbResult.Event {
		case odoov1.ReasonDatabaseCreated:
			metrics.DatabaseOperation(metrics.DatabaseCreate, metrics.DatabaseSucceeded)
		case odoov1.ReasonDatabaseAdopted:
			metrics.DatabaseOperation(metrics.DatabaseAdopt, metrics.DatabaseSucceeded)
		}
	}
	s.condition(odoov1.ConditionDatabaseReady, metav1.ConditionTrue, dbResult.Reason, dbResult.Message)
	// An adopted (external) database releases the database finalizer.
	if err := s.syncFinalizers(ctx); err != nil {
		return ctrl.Result{}, false, err
	}
	return ctrl.Result{}, true, nil
}

// wantsFinalizer: the operator may have to drop the database.
func wantsFinalizer(od *odoov1.OdooDeployment) bool {
	if od.Spec.Database.DeletionPolicyValue() != odoov1.DeletionPolicyDelete {
		return false
	}
	switch od.Status.Database.ProvisionedBy {
	case "", odoov1.DatabaseProvisionedByOperator:
		return true
	default:
		return false
	}
}

// wantsOperationsFinalizer: no database finalizer, and a Job of the instance
// may run.
func wantsOperationsFinalizer(od *odoov1.OdooDeployment) bool {
	return !wantsFinalizer(od) && (od.Status.CurrentOperation.Kind != "" || od.Status.CurrentInitJob.Name != "")
}

// observeCurrentJob handles status.currentInitJob. done is true when the
// reconcile may continue (the Job succeeded or vanished and was cleared).
func (r *OdooDeploymentReconciler) observeCurrentJob(ctx context.Context, s *reconcileState) (ctrl.Result, bool, error) {
	logger := log.FromContext(ctx)
	od := s.od
	current := od.Status.CurrentInitJob
	isInit := current.Kind != odoov1.JobKindUpgrade
	runningPhase, runningReason := odoov1.PhaseUpgrading, odoov1.ReasonUpgradeJobRunning
	failedReason, succeededReason := odoov1.ReasonUpgradeJobFailed, odoov1.ReasonUpgradeJobSucceeded
	if isInit {
		runningPhase, runningReason = odoov1.PhaseInitializing, odoov1.ReasonInitJobRunning
		failedReason, succeededReason = odoov1.ReasonInitJobFailed, odoov1.ReasonInitJobSucceeded
	}

	obs, err := reconcileloops.ObserveMaintenanceJob(ctx, r.Client, r.reader(), od)
	if err != nil {
		result, err := s.fail(odoov1.ReasonReconcileFailed, err)
		return result, false, err
	}
	switch obs.State {
	case reconcileloops.JobStateMissing:
		logger.Info("Maintenance job disappeared, it will be recreated", "job", current.Name)
		od.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{}
		return ctrl.Result{RequeueAfter: time.Second}, false, nil

	case reconcileloops.JobStateSucceeded:
		logger.Info("Maintenance job succeeded", "job", current.Name, "installed", current.Modules, "upgraded", current.UpgradeModules)
		installed := append([]string{}, od.Status.InitModulesInstalled...)
		installed = append(installed, current.Modules...)
		od.Status.InitModulesInstalled = dedupe(installed)
		if current.Image != "" {
			od.Status.AppliedImage = current.Image
		}
		od.Status.AppliedUpgradeToken = current.Token
		took := jobDuration(current, obs.Job)
		if current.Kind == odoov1.JobKindUpgrade && took > 0 {
			od.Status.LastMaintenanceDuration = &metav1.Duration{Duration: took}
		}
		metrics.JobFinished(od.Namespace, od.Name, current.Kind, metrics.JobSucceeded, took)
		if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, obs.Job); err != nil {
			result, err := s.fail(odoov1.ReasonReconcileFailed, err)
			return result, false, err
		}
		od.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{}
		r.event(od, corev1.EventTypeNormal, succeededReason, fmt.Sprintf("job %s succeeded", current.Name))
		return ctrl.Result{}, true, nil

	case reconcileloops.JobStateFailed:
		if err := reconcileloops.KeepFailedMaintenanceJob(ctx, r.Client, obs.Job); err != nil {
			result, err := s.fail(odoov1.ReasonReconcileFailed, err)
			return result, false, err
		}
		reason := failedReason
		detail := obs.FailureMessage
		if obs.QuotaMessage != "" {
			reason = odoov1.ReasonQuotaExceeded
			detail = obs.QuotaMessage
		}
		msg := fmt.Sprintf("job %s failed (%s); inspect with: kubectl -n %s logs job/%s — delete the Job to retry",
			current.Name, detail, od.Namespace, current.Name)
		// This branch runs again on every requeue until the Job is deleted;
		// the Job is counted on the reconcile that first saw it fail.
		if s.start.Phase != odoov1.PhaseFailed {
			metrics.JobFinished(od.Namespace, od.Name, current.Kind, metrics.JobFailed, jobDuration(current, obs.Job))
		}
		od.Status.Phase = odoov1.PhaseFailed
		s.degraded(reason, msg)
		s.ready(metav1.ConditionFalse, reason, msg)
		r.event(od, corev1.EventTypeWarning, reason, msg)
		return ctrl.Result{RequeueAfter: 2 * time.Minute}, false, nil

	default: // active
		if obs.ImagePullMessage != "" && od.Spec.Image != current.Image {
			// The pod never started, so nothing touched the database: replace
			// the Job with one for the image spec.image now names.
			msg := fmt.Sprintf("job %s cannot pull %s (%s); replacing it for %s",
				current.Name, current.Image, obs.ImagePullMessage, od.Spec.Image)
			logger.Info(msg)
			if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, obs.Job); err != nil {
				result, err := s.fail(odoov1.ReasonReconcileFailed, err)
				return result, false, err
			}
			od.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{}
			r.event(od, corev1.EventTypeNormal, odoov1.ReasonJobSuperseded, msg)
			metrics.JobWasSuperseded(od.Namespace, od.Name, current.Kind)
			return ctrl.Result{RequeueAfter: time.Second}, false, nil
		}
		switch {
		case obs.ImagePullMessage != "":
			msg := fmt.Sprintf("job %s cannot pull %s (%s); it starts once the image can be pulled or spec.image changes",
				current.Name, current.Image, obs.ImagePullMessage)
			od.Status.Phase = runningPhase
			s.degraded(odoov1.ReasonImagePullFailed, msg)
			s.ready(metav1.ConditionFalse, odoov1.ReasonImagePullFailed, msg)
		case obs.QuotaMessage != "":
			od.Status.Phase = odoov1.PhasePending
			s.degraded(odoov1.ReasonQuotaExceeded, obs.QuotaMessage)
			s.ready(metav1.ConditionFalse, odoov1.ReasonQuotaExceeded, obs.QuotaMessage)
		case obs.StartMessage != "":
			msg := fmt.Sprintf("job %s cannot start: %s", current.Name, obs.StartMessage)
			od.Status.Phase = runningPhase
			s.degraded(odoov1.ReasonJobCannotStart, msg)
			s.ready(metav1.ConditionFalse, odoov1.ReasonJobCannotStart, msg)
		default:
			od.Status.Phase = runningPhase
			s.healthy()
			s.ready(metav1.ConditionFalse, runningReason, fmt.Sprintf("waiting for job %s", current.Name))
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, false, nil
	}
}

// startMaintenanceJob scales the Deployment to zero (never on first boot),
// waits for its pods to be gone and creates the init/upgrade Job.
func (r *OdooDeploymentReconciler) startMaintenanceJob(
	ctx context.Context,
	s *reconcileState,
	configHash string,
	initModules, upgradeModules []string,
	firstBoot bool,
) (ctrl.Result, error) {
	od := s.od
	kind, phase, createdReason := odoov1.JobKindUpgrade, odoov1.PhaseUpgrading, odoov1.ReasonUpgradeJobCreated
	if firstBoot {
		kind, phase, createdReason = odoov1.JobKindInit, odoov1.PhaseInitializing, odoov1.ReasonInitJobCreated
	}

	if !firstBoot {
		if _, err := reconcileloops.EnsureDeployment(ctx, r.Client, r.Scheme, od, od.Status.AppliedImage, 0, configHash); err != nil {
			return s.fail(odoov1.ReasonDeploymentFailed, err)
		}
		running, err := r.countPods(ctx, od.Namespace, od.GetServiceSelectorLabels())
		if err != nil {
			return s.fail(odoov1.ReasonReconcileFailed, err)
		}
		if running > 0 {
			msg := fmt.Sprintf("waiting for %d pod(s) to stop before running the %s job", running, kind)
			od.Status.Phase = phase
			s.healthy()
			s.ready(metav1.ConditionFalse, odoov1.ReasonWaitingForPodsToStop, msg)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}

	if err := s.holdForJob(ctx); err != nil {
		return s.fail(odoov1.ReasonReconcileFailed, err)
	}
	job, err := reconcileloops.EnsureMaintenanceJob(ctx, r.Client, r.Scheme, od, initModules, upgradeModules, firstBoot)
	if errors.Is(err, reconcileloops.ErrJobTerminating) {
		od.Status.Phase = phase
		s.ready(metav1.ConditionFalse, createdReason, "waiting for the replaced job to be deleted")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err != nil {
		return s.fail(odoov1.ReasonJobCreationFailed, err)
	}
	startedAt := job.CreationTimestamp
	if startedAt.IsZero() {
		startedAt = metav1.Now()
	}
	od.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{
		StartedAt:      &startedAt,
		Name:           job.Name,
		Namespace:      job.Namespace,
		Kind:           kind,
		Image:          od.Spec.Image,
		Token:          od.Spec.Upgrade.Token,
		Modules:        initModules,
		UpgradeModules: upgradeModules,
	}
	od.Status.Phase = phase
	s.healthy()
	msg := fmt.Sprintf("job %s created (install: %v, upgrade: %v, image: %s)", job.Name, initModules, upgradeModules, od.Spec.Image)
	s.ready(metav1.ConditionFalse, createdReason, msg)
	r.event(od, corev1.EventTypeNormal, createdReason, msg)
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

// reconcileDelete stops the workload, waits for the pods of the instance's
// Jobs, drops the database when, and only when, the operator created it and
// deletionPolicy is Delete, then removes the finalizers.
func (r *OdooDeploymentReconciler) reconcileDelete(ctx context.Context, s *reconcileState) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	od := s.od
	dropping := controllerutil.ContainsFinalizer(od, odoov1.DatabaseFinalizer)
	if !dropping && !controllerutil.ContainsFinalizer(od, odoov1.OperationsFinalizer) {
		// Children are garbage collected through their owner references.
		return ctrl.Result{}, nil
	}
	s.ready(metav1.ConditionFalse, odoov1.ReasonDeleting, "OdooDeployment is being deleted")

	propagation := metav1.DeletePropagationBackground
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: od.Name, Namespace: od.Namespace}}
	if err := r.Delete(ctx, deployment, &client.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !apierrors.IsNotFound(err) {
		return s.fail(odoov1.ReasonDeleting, fmt.Errorf("delete deployment: %w", err))
	}
	if dropping && od.Status.CurrentInitJob.Name != "" {
		// The database it initializes or upgrades is dropped anyway; a
		// database that stays is never left half upgraded, so its Job is
		// waited for below.
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: od.Status.CurrentInitJob.Name, Namespace: od.Namespace}}
		if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, job); err != nil {
			return s.fail(odoov1.ReasonDeleting, err)
		}
	}
	// An image check mounts nothing and changes nothing, so it is never
	// waited for, whether its pod is stuck pulling or its container runs.
	if err := reconcileloops.PruneImageCheckJobs(ctx, r.Client, od, ""); err != nil {
		return s.fail(odoov1.ReasonDeleting, err)
	}
	if err := r.deleteJobsThatNeverRan(ctx, od); err != nil {
		return s.fail(odoov1.ReasonDeleting, err)
	}
	appPods, err := r.countPods(ctx, od.Namespace, od.GetServiceSelectorLabels())
	if err != nil {
		return s.fail(odoov1.ReasonDeleting, err)
	}
	ownedPods, err := r.countPods(ctx, od.Namespace, map[string]string{odoov1.LabelOdooDeployment: od.Name})
	if err != nil {
		return s.fail(odoov1.ReasonDeleting, err)
	}
	if appPods+ownedPods > 0 {
		logger.Info("Waiting for pods to stop before releasing the instance", "pods", appPods+ownedPods)
		s.ready(metav1.ConditionFalse, odoov1.ReasonDeleting,
			fmt.Sprintf("OdooDeployment is being deleted; waiting for %d pod(s) to stop", appPods+ownedPods))
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	w, err := r.listWork(ctx, od)
	if err != nil {
		return s.fail(odoov1.ReasonDeleting, err)
	}
	if err := r.settleBackupOnDelete(ctx, s, w); err != nil {
		return s.fail(odoov1.ReasonDeleting, err)
	}
	if !dropping {
		if err := r.releaseRestores(ctx, w); err != nil {
			return s.fail(odoov1.ReasonDeleting, err)
		}
		return ctrl.Result{}, s.setFinalizers(ctx, false, false)
	}

	conn, connErr := od.Spec.Database.GetDbConnectionDetails(r.Client, ctx, od.Namespace)
	db := od.Status.Database
	drop := connErr == nil &&
		od.Spec.Database.DeletionPolicyValue() == odoov1.DeletionPolicyDelete &&
		db.ProvisionedBy == odoov1.DatabaseProvisionedByOperator &&
		db.Name != "" && db.Name == conn.Name
	switch {
	case drop:
		tag := database.NewOwnerTag(od.Namespace, od.Name)
		if wait, err := r.dropWorkData(ctx, s, w, reconcileloops.ConnInfoFor(od, conn), tag); err != nil || wait > 0 {
			if err != nil {
				return s.fail(odoov1.ReasonDeleting, err)
			}
			return ctrl.Result{RequeueAfter: wait}, nil
		}
		err := r.DB.Drop(ctx, reconcileloops.ConnInfoFor(od, conn), db.Name, tag)
		switch {
		case errors.Is(err, database.ErrNotOwned):
			msg := fmt.Sprintf("refusing to drop database %q: %v", db.Name, err)
			logger.Info(msg)
			r.event(od, corev1.EventTypeWarning, odoov1.ReasonDatabaseDropRefused, msg)
			metrics.DatabaseOperation(metrics.DatabaseDrop, metrics.DatabaseRefused)
		case err != nil:
			msg := fmt.Sprintf("could not drop database %q: %v", db.Name, err)
			logger.Error(err, "database drop failed", "database", db.Name)
			s.degraded(odoov1.ReasonDatabaseDropFailed, msg)
			r.event(od, corev1.EventTypeWarning, odoov1.ReasonDatabaseDropFailed, msg)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		default:
			msg := fmt.Sprintf("dropped database %q on %s", db.Name, conn.Host)
			logger.Info(msg)
			r.event(od, corev1.EventTypeNormal, odoov1.ReasonDatabaseDropped, msg)
			metrics.DatabaseOperation(metrics.DatabaseDrop, metrics.DatabaseSucceeded)
		}
	case connErr != nil && db.ProvisionedBy == odoov1.DatabaseProvisionedByOperator:
		// Leaking a database is the safe direction.
		msg := fmt.Sprintf("database %q not dropped: could not resolve the connection: %v", db.Name, connErr)
		logger.Info(msg)
		r.event(od, corev1.EventTypeWarning, odoov1.ReasonDatabaseNotDropped, msg)
	}

	if err := r.releaseRestores(ctx, w); err != nil {
		return s.fail(odoov1.ReasonDeleting, err)
	}
	return ctrl.Result{}, s.setFinalizers(ctx, false, false)
}

// deleteJobsThatNeverRan deletes the instance's unfinished Jobs none of whose
// pods ever started a container: nothing of them ran, so nothing is cut
// short, and a Job stuck before its first container (an image that cannot
// be pulled, a quota) would otherwise hold the deletion for ever. Jobs that
// ran anything are left to finish, and so are the cleanup Jobs the deletion
// itself starts, which their deadline bounds.
func (r *OdooDeploymentReconciler) deleteJobsThatNeverRan(ctx context.Context, od *odoov1.OdooDeployment) error {
	jobs := &batchv1.JobList{}
	if err := r.reader().List(ctx, jobs, client.InNamespace(od.Namespace),
		client.MatchingLabels{odoov1.LabelOdooDeployment: od.Name}); err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}
	for i := range jobs.Items {
		j := &jobs.Items[i]
		if !metav1.IsControlledBy(j, od) || jobFinished(j) || j.Status.Failed+j.Status.Succeeded > 0 {
			continue
		}
		if j.Annotations[annotationStartedByDeletion] != "" {
			// The deletion's own cleanup Job, which its deadline bounds.
			continue
		}
		pods := &corev1.PodList{}
		if err := r.reader().List(ctx, pods, client.InNamespace(od.Namespace), client.MatchingLabels{"job-name": j.Name}); err != nil {
			return fmt.Errorf("list pods of job %s: %w", j.Name, err)
		}
		ran := false
		for p := range pods.Items {
			ran = ran || reconcileloops.PodRanAnyContainer(&pods.Items[p])
		}
		if ran {
			continue
		}
		log.FromContext(ctx).Info("Deleting a job that never ran", "job", j.Name)
		if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, j); err != nil {
			return err
		}
	}
	return nil
}

// jobDuration is how long a finished maintenance Job took, from its creation
// (status.currentInitJob.startedAt) to its completion, rounded to the second.
// A failed Job has no completionTime; its Failed condition marks the end.
func jobDuration(current odoov1.MaintenanceJobStatus, job *batchv1.Job) time.Duration {
	start := current.StartedAt
	if start == nil && job != nil {
		start = job.Status.StartTime
	}
	if start == nil {
		return 0
	}
	end := time.Now()
	if job != nil {
		if job.Status.CompletionTime != nil {
			end = job.Status.CompletionTime.Time
		} else {
			for _, c := range job.Status.Conditions {
				if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue && !c.LastTransitionTime.IsZero() {
					end = c.LastTransitionTime.Time
				}
			}
		}
	}
	return end.Sub(start.Time).Round(time.Second)
}

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// SetupWithManager sets up the controller with the Manager.
func (r *OdooDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&odoov1.OdooDeployment{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&batchv1.Job{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.mapSecretsToOdooDeployments()),
			builder.WithPredicates(secretsPredicate),
		).
		Watches(
			&corev1.PersistentVolumeClaim{},
			handler.EnqueueRequestsFromMapFunc(r.mapPVCsToOdooDeployments()),
			builder.WithPredicates(pvcPredicate),
		).
		Watches(
			&appsv1.Deployment{},
			handler.EnqueueRequestsFromMapFunc(r.mapDeploymentsToOdooDeployments()),
			builder.WithPredicates(deploymentPredicate),
		).
		Watches(
			&corev1.Service{},
			handler.EnqueueRequestsFromMapFunc(r.mapServicesToOdooDeployments()),
			builder.WithPredicates(servicePredicate),
		).
		Watches(&odoov1.OdooBackup{}, handler.EnqueueRequestsFromMapFunc(workToOdooDeployment)).
		Watches(&odoov1.OdooRestore{}, handler.EnqueueRequestsFromMapFunc(workToOdooDeployment)).
		Watches(&odoov1.OdooBackupStore{}, handler.EnqueueRequestsFromMapFunc(r.storeToOdooDeployments)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
		Complete(r)
}

// mapSecretsToOdooDeployments returns a function mapping OdooDeployment events watched to OdooDeployment reconcile requests
func (r *OdooDeploymentReconciler) mapSecretsToOdooDeployments() handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		secret, ok := obj.(*corev1.Secret)
		if !ok {
			return nil
		}
		odooDeployments, err := r.getOdooDeploymentsForSecretsOrConfigMapsToOdooDeploymentsMapper(ctx, secret)
		if err != nil {
			log.FromContext(ctx).Error(err, "while getting OdooDeployment list", "namespace", secret.Namespace)
			return nil
		}
		// build requests for OdooDeployment referring the secret
		return filterOdooDeploymentsUsingSecret(odooDeployments, secret)
	}
}

func (r *OdooDeploymentReconciler) mapPVCsToOdooDeployments() handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		pvc, ok := obj.(*corev1.PersistentVolumeClaim)
		if !ok {
			return nil
		}
		odooDeployments, err := r.getOdooDeploymentsForPVCsToOdooDeploymentsMapper(ctx, pvc)
		if err != nil {
			log.FromContext(ctx).Error(err, "while getting OdooDeployment list", "namespace", pvc.Namespace)
			return nil
		}
		// build requests for OdooDeployment referring the PVC
		return filterOdooDeploymentsUsingPVC(odooDeployments, pvc)
	}
}
func (r *OdooDeploymentReconciler) mapDeploymentsToOdooDeployments() handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		deployment, ok := obj.(*appsv1.Deployment)
		if !ok {
			return nil
		}
		odooDeployments, err := r.getOdooDeploymentsForDeploymentsToOdooDeploymentsMapper(ctx, deployment)
		if err != nil {
			log.FromContext(ctx).Error(err, "while getting OdooDeployment list", "namespace", deployment.Namespace)
			return nil
		}
		// build requests for OdooDeployment referring the Deployment
		return filterOdooDeploymentsUsingDeployment(odooDeployments, deployment)
	}
}
func (r *OdooDeploymentReconciler) mapServicesToOdooDeployments() handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		service, ok := obj.(*corev1.Service)
		if !ok {
			return nil
		}
		odooDeployments, err := r.getOdooDeploymentsForServicesToOdooDeploymentsMapper(ctx, service)
		if err != nil {
			log.FromContext(ctx).Error(err, "while getting OdooDeployment list", "namespace", service.Namespace)
			return nil
		}
		// build requests for OdooDeployment referring the Service
		return filterOdooDeploymentsUsingService(odooDeployments, service)
	}
}

func (r *OdooDeploymentReconciler) getOdooDeploymentsForSecretsOrConfigMapsToOdooDeploymentsMapper(
	ctx context.Context,
	object metav1.Object,
) (odooDeployments odoov1.OdooDeploymentList, err error) {
	_, isSecret := object.(*corev1.Secret)
	_, isConfigMap := object.(*corev1.ConfigMap)

	if !isSecret && !isConfigMap {
		return odooDeployments, fmt.Errorf("unsupported object: %+v", object)
	}

	// Get all the Odoo Deployments handled by the operator in the secret namespaces
	err = r.List(
		ctx,
		&odooDeployments,
		client.InNamespace(object.GetNamespace()),
	)
	return odooDeployments, err
}

func (r *OdooDeploymentReconciler) getOdooDeploymentsForPVCsToOdooDeploymentsMapper(
	ctx context.Context,
	object metav1.Object,
) (odooDeployments odoov1.OdooDeploymentList, err error) {
	_, isPVC := object.(*corev1.PersistentVolumeClaim)

	if !isPVC {
		return odooDeployments, fmt.Errorf("unsupported object: %+v", object)
	}

	// Get all the Odoo Deployments handled by the operator in the PVC namespaces
	err = r.List(
		ctx,
		&odooDeployments,
		client.InNamespace(object.GetNamespace()),
	)
	return odooDeployments, err
}

func (r *OdooDeploymentReconciler) getOdooDeploymentsForDeploymentsToOdooDeploymentsMapper(
	ctx context.Context,
	object metav1.Object,
) (odooDeployments odoov1.OdooDeploymentList, err error) {
	_, isDeployment := object.(*appsv1.Deployment)

	if !isDeployment {
		return odooDeployments, fmt.Errorf("unsupported object: %+v", object)
	}

	// Get all the Odoo Deployments handled by the operator in the Deployment namespaces
	err = r.List(
		ctx,
		&odooDeployments,
		client.InNamespace(object.GetNamespace()),
	)
	return odooDeployments, err
}

func (r *OdooDeploymentReconciler) getOdooDeploymentsForServicesToOdooDeploymentsMapper(
	ctx context.Context,
	object metav1.Object,
) (odooDeployments odoov1.OdooDeploymentList, err error) {
	_, isService := object.(*corev1.Service)

	if !isService {
		return odooDeployments, fmt.Errorf("unsupported object: %+v", object)
	}

	// Get all the Odoo Deployments handled by the operator in the Service namespaces
	err = r.List(
		ctx,
		&odooDeployments,
		client.InNamespace(object.GetNamespace()),
	)
	return odooDeployments, err
}

// filterOdooDeploymentsUsingSecret returns a list of reconcile.Request for the Odoo Deployments
// that reference the secret
func filterOdooDeploymentsUsingSecret(
	odooDeployments odoov1.OdooDeploymentList,
	secret *corev1.Secret,
) (requests []reconcile.Request) {
	for _, deployment := range odooDeployments.Items {
		if deployment.UsesSecret(secret.Name) {
			requests = append(requests,
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      deployment.Name,
						Namespace: deployment.Namespace,
					},
				},
			)
			continue
		}
	}
	return requests
}

// filterOdooDeploymentsUsingPVC returns a list of reconcile.Request for the Odoo Deployments
// that reference the PVC
func filterOdooDeploymentsUsingPVC(
	odooDeployments odoov1.OdooDeploymentList,
	pvc *corev1.PersistentVolumeClaim,
) (requests []reconcile.Request) {
	for _, deployment := range odooDeployments.Items {
		if deployment.UsesPVC(pvc.Name) {
			requests = append(requests,
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      deployment.Name,
						Namespace: deployment.Namespace,
					},
				},
			)
			continue
		}
	}
	return requests
}

// filterOdooDeploymentsUsingDeployment returns a list of reconcile.Request for the Odoo Deployments
// that reference the Deployment
func filterOdooDeploymentsUsingDeployment(
	odooDeployments odoov1.OdooDeploymentList,
	deployment *appsv1.Deployment,
) (requests []reconcile.Request) {
	for _, odooDeployment := range odooDeployments.Items {
		if odooDeployment.UsesDeployment(deployment.Name) {
			requests = append(requests,
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      odooDeployment.Name,
						Namespace: odooDeployment.Namespace,
					},
				},
			)
			continue
		}
	}
	return requests
}

// filterOdooDeploymentsUsingService returns a list of reconcile.Request for the Odoo Deployments
// that reference the Service
func filterOdooDeploymentsUsingService(
	odooDeployments odoov1.OdooDeploymentList,
	service *corev1.Service,
) (requests []reconcile.Request) {
	for _, odooDeployment := range odooDeployments.Items {
		if odooDeployment.UsesService(service.Name) {
			requests = append(requests,
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      odooDeployment.Name,
						Namespace: odooDeployment.Namespace,
					},
				},
			)
			continue
		}
	}
	return requests
}

// workToOdooDeployment maps an OdooBackup or OdooRestore to its instance.
func workToOdooDeployment(_ context.Context, obj client.Object) []reconcile.Request {
	var name string
	switch o := obj.(type) {
	case *odoov1.OdooBackup:
		name = o.Spec.OdooDeployment
	case *odoov1.OdooRestore:
		name = o.Spec.OdooDeployment
	}
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: name}}}
}

// storeToOdooDeployments maps a store to the instances that back up to it,
// so a store turning ready starts their waiting backups.
func (r *OdooDeploymentReconciler) storeToOdooDeployments(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &odoov1.OdooDeploymentList{}
	if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		for _, s := range list.Items[i].Spec.Backup.Stores {
			if s == obj.GetName() {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
				break
			}
		}
	}
	return reqs
}
