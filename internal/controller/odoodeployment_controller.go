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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	"github.com/MohanadAbugharbia/odoo-operator/internal/controller/reconcileloops"
	"github.com/MohanadAbugharbia/odoo-operator/internal/database"
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
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// reconcileState carries one reconcile's working copy of the OdooDeployment
// and the snapshot the deferred status patch is computed against.
type reconcileState struct {
	r    *OdooDeploymentReconciler
	od   *odoov1.OdooDeployment
	base *odoov1.OdooDeployment
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

// setFinalizer adds or removes the database finalizer with a metadata-only
// patch on a copy, so the in-memory status is never overwritten by the
// API-server response.
func (s *reconcileState) setFinalizer(ctx context.Context, want bool) error {
	has := controllerutil.ContainsFinalizer(s.od, odoov1.DatabaseFinalizer)
	if has == want {
		return nil
	}
	patched := s.od.DeepCopy()
	if want {
		controllerutil.AddFinalizer(patched, odoov1.DatabaseFinalizer)
	} else {
		controllerutil.RemoveFinalizer(patched, odoov1.DatabaseFinalizer)
	}
	if err := s.r.Patch(ctx, patched, client.MergeFrom(s.od)); err != nil {
		return fmt.Errorf("patch finalizer: %w", err)
	}
	s.od.ObjectMeta = patched.ObjectMeta
	s.base.ObjectMeta = *patched.ObjectMeta.DeepCopy()
	log.FromContext(ctx).Info("Updated database finalizer", "present", want)
	return nil
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

// countPods returns the number of pods (terminating ones included) matching labels.
func (r *OdooDeploymentReconciler) countPods(ctx context.Context, namespace string, labels map[string]string) (int, error) {
	pods := &corev1.PodList{}
	if err := r.reader().List(ctx, pods, client.InNamespace(namespace), client.MatchingLabels(labels)); err != nil {
		return 0, fmt.Errorf("list pods: %w", err)
	}
	return len(pods.Items), nil
}

// Reconcile drives an OdooDeployment through Pending → Initializing →
// Running, runs upgrade Jobs on image/token changes and drops the database
// on deletion when the operator created it.
func (r *OdooDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := log.FromContext(ctx)

	od := &odoov1.OdooDeployment{}
	if err := r.Get(ctx, req.NamespacedName, od); err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("OdooDeployment resource object not found.")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get OdooDeployment: %w", err)
	}
	logger.V(1).Info("Reconciling OdooDeployment", "resourceVersion", od.ResourceVersion, "generation", od.Generation)

	od.DeduplicateModules()
	s := &reconcileState{r: r, od: od, base: od.DeepCopy()}
	defer func() {
		if patchErr := s.patchStatus(ctx); patchErr != nil {
			err = utilerrors.NewAggregate([]error{err, patchErr})
		}
	}()

	if !od.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, s)
	}
	return r.reconcileNormal(ctx, s)
}

func (r *OdooDeploymentReconciler) reconcileNormal(ctx context.Context, s *reconcileState) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	od := s.od

	// The finalizer is only needed while the operator may own the database:
	// deletionPolicy Delete and provisionedBy not yet known or "operator".
	if err := s.setFinalizer(ctx, wantsFinalizer(od)); err != nil {
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

	// Database.
	if result, ready, err := r.settleDatabase(ctx, s, conn); err != nil || !ready {
		return result, err
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

	if needJob {
		return r.startMaintenanceJob(ctx, s, configHash, initModules, upgradeModules, firstBoot)
	}
	if plainRoll {
		logger.Info("Rolling out new image without an upgrade Job", "from", od.Status.AppliedImage, "to", od.Spec.Image)
		od.Status.AppliedImage = od.Spec.Image
		od.Status.AppliedUpgradeToken = od.Spec.Upgrade.Token
	}

	// Steady state.
	replicas := od.Spec.ReplicasValue()
	deployment, err := reconcileloops.EnsureDeployment(ctx, r.Client, r.Scheme, od, od.Status.AppliedImage, replicas, configHash)
	if err != nil {
		return s.fail(odoov1.ReasonDeploymentFailed, err)
	}
	if _, err := reconcileloops.EnsureHttpService(ctx, r.Client, r.Scheme, od); err != nil {
		return s.fail(odoov1.ReasonFailedCreateHttpService, err)
	}
	if _, err := reconcileloops.EnsurePollService(ctx, r.Client, r.Scheme, od); err != nil {
		return s.fail(odoov1.ReasonFailedCreatePollService, err)
	}

	od.Status.Phase = odoov1.PhaseRunning
	od.Status.ReadyReplicas = deployment.Status.AvailableReplicas
	s.healthy()

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
	return ctrl.Result{}, nil
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
	}
	s.condition(odoov1.ConditionDatabaseReady, metav1.ConditionTrue, dbResult.Reason, dbResult.Message)
	// An adopted (external) database releases the finalizer.
	if err := s.setFinalizer(ctx, wantsFinalizer(od)); err != nil {
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
		if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, obs.Job); err != nil {
			result, err := s.fail(odoov1.ReasonReconcileFailed, err)
			return result, false, err
		}
		od.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{}
		r.event(od, corev1.EventTypeNormal, succeededReason, fmt.Sprintf("job %s succeeded", current.Name))
		return ctrl.Result{}, true, nil

	case reconcileloops.JobStateFailed:
		reason := failedReason
		detail := obs.FailureMessage
		if obs.QuotaMessage != "" {
			reason = odoov1.ReasonQuotaExceeded
			detail = obs.QuotaMessage
		}
		msg := fmt.Sprintf("job %s failed (%s); inspect with: kubectl -n %s logs job/%s — delete the Job to retry",
			current.Name, detail, od.Namespace, current.Name)
		od.Status.Phase = odoov1.PhaseFailed
		s.degraded(reason, msg)
		s.ready(metav1.ConditionFalse, reason, msg)
		r.event(od, corev1.EventTypeWarning, reason, msg)
		return ctrl.Result{RequeueAfter: 2 * time.Minute}, false, nil

	default: // active
		if obs.QuotaMessage != "" {
			od.Status.Phase = odoov1.PhasePending
			s.degraded(odoov1.ReasonQuotaExceeded, obs.QuotaMessage)
			s.ready(metav1.ConditionFalse, odoov1.ReasonQuotaExceeded, obs.QuotaMessage)
		} else {
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

	job, err := reconcileloops.EnsureMaintenanceJob(ctx, r.Client, r.Scheme, od, initModules, upgradeModules, firstBoot)
	if err != nil {
		return s.fail(odoov1.ReasonJobCreationFailed, err)
	}
	od.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{
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

// reconcileDelete stops the workload and drops the database when, and only
// when, the operator created it, then removes the finalizer.
func (r *OdooDeploymentReconciler) reconcileDelete(ctx context.Context, s *reconcileState) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	od := s.od
	if !controllerutil.ContainsFinalizer(od, odoov1.DatabaseFinalizer) {
		// Children are garbage collected through their owner references.
		return ctrl.Result{}, nil
	}
	s.ready(metav1.ConditionFalse, odoov1.ReasonDeleting, "OdooDeployment is being deleted")

	propagation := metav1.DeletePropagationBackground
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: od.Name, Namespace: od.Namespace}}
	if err := r.Delete(ctx, deployment, &client.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !apierrors.IsNotFound(err) {
		return s.fail(odoov1.ReasonDeleting, fmt.Errorf("delete deployment: %w", err))
	}
	if od.Status.CurrentInitJob.Name != "" {
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: od.Status.CurrentInitJob.Name, Namespace: od.Namespace}}
		if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, job); err != nil {
			return s.fail(odoov1.ReasonDeleting, err)
		}
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
		logger.Info("Waiting for pods to stop before dropping the database", "pods", appPods+ownedPods)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
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
		err := r.DB.Drop(ctx, reconcileloops.ConnInfoFor(od, conn), db.Name, tag)
		switch {
		case errors.Is(err, database.ErrNotOwned):
			msg := fmt.Sprintf("refusing to drop database %q: %v", db.Name, err)
			logger.Info(msg)
			r.event(od, corev1.EventTypeWarning, odoov1.ReasonDatabaseDropRefused, msg)
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
		}
	case connErr != nil && db.ProvisionedBy == odoov1.DatabaseProvisionedByOperator:
		// Leaking a database is the safe direction.
		msg := fmt.Sprintf("database %q not dropped: could not resolve the connection: %v", db.Name, connErr)
		logger.Info(msg)
		r.event(od, corev1.EventTypeWarning, odoov1.ReasonDatabaseNotDropped, msg)
	}

	return ctrl.Result{}, s.setFinalizer(ctx, false)
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
