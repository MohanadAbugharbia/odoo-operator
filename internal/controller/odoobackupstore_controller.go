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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/jobspec"
	"github.com/MohanadAbugharbia/odoo-operator/internal/controller/reconcileloops"
	"github.com/MohanadAbugharbia/odoo-operator/pkg/utils"
)

// Store probe reasons.
const (
	ReasonStoreReachable    = "StoreReachable"
	ReasonStoreUnreachable  = "StoreUnreachable"
	ReasonStoreProbing      = "Probing"
	ReasonStoreSecretBroken = "SecretMissing"
	ReasonStoreInvalid      = "InvalidSpec"
)

// StoreProbeInterval is how often a store is probed again.
const StoreProbeInterval = 6 * time.Hour

// OdooBackupStoreReconciler probes stores: a short Job writes, reads and
// deletes a file in the store and the result becomes status.ready.
type OdooBackupStoreReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	APIReader client.Reader
	Recorder  record.EventRecorder
	// Image returns the backup image.
	Image func(context.Context) (string, error)
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoobackupstores,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoobackupstores/status,verbs=get;update;patch

func (r *OdooBackupStoreReconciler) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *OdooBackupStoreReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// probeHash identifies what a probe tests: the spec, the Secrets' versions
// and the probe period, so a change or the next period starts a new probe.
func (r *OdooBackupStoreReconciler) probeHash(ctx context.Context, store *odoov1.OdooBackupStore) (string, error) {
	spec, err := json.Marshal(store.Spec)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(spec)
	secrets := jobspec.StoreSecrets(store)
	names := make([]string, 0, len(secrets))
	for n := range secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: store.Namespace, Name: n}, secret); err != nil {
			return "", err
		}
		fmt.Fprintf(h, "\x00%s=%s", n, secret.ResourceVersion)
	}
	period := r.now().Unix() / int64(StoreProbeInterval/time.Second)
	fmt.Fprintf(h, "\x00%s", strconv.FormatInt(period, 10))
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// Reconcile probes the store when its spec, its Secrets or the probe period changed.
func (r *OdooBackupStoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	store := &odoov1.OdooBackupStore{}
	if err := r.Get(ctx, req.NamespacedName, store); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !store.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	base := store.DeepCopy()
	result, err := r.reconcile(ctx, store)
	store.Status.ObservedGeneration = store.Generation
	if !equality.Semantic.DeepEqual(base.Status, store.Status) {
		if patchErr := r.Status().Patch(ctx, store, client.MergeFrom(base)); patchErr != nil && !apierrors.IsNotFound(patchErr) {
			return ctrl.Result{}, patchErr
		}
	}
	return result, err
}

func (r *OdooBackupStoreReconciler) setReady(store *odoov1.OdooBackupStore, ready bool, reason, message string) {
	store.Status.Ready = ready
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	utils.SetCondition(&store.Status.Conditions, odoov1.ConditionReady, status, reason, message, store.Generation)
}

func (r *OdooBackupStoreReconciler) reconcile(ctx context.Context, store *odoov1.OdooBackupStore) (ctrl.Result, error) {
	if _, err := jobspec.Locate(store, ""); err != nil {
		r.setReady(store, false, ReasonStoreInvalid, err.Error())
		return ctrl.Result{}, nil
	}
	if err := checkStoreSecrets(ctx, r.Client, store); err != nil {
		r.setReady(store, false, ReasonStoreSecretBroken, err.Error())
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	hash, err := r.probeHash(ctx, store)
	if err != nil {
		return ctrl.Result{}, err
	}
	nextPeriod := time.Until(time.Unix((r.now().Unix()/int64(StoreProbeInterval/time.Second)+1)*int64(StoreProbeInterval/time.Second), 0))
	if store.Status.ProbedHash == hash && store.Status.ProbeJob == "" {
		return ctrl.Result{RequeueAfter: nextPeriod}, nil
	}
	if store.Status.ProbeJob == "" || store.Status.ProbedHash != hash {
		image, err := r.Image(ctx)
		if err != nil {
			r.setReady(store, store.Status.Ready, ReasonBackupImageUnknown, err.Error())
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		job, err := jobspec.ProbeJob(image, store, hash)
		if err != nil {
			r.setReady(store, false, ReasonStoreInvalid, err.Error())
			return ctrl.Result{}, nil
		}
		if err := controllerutil.SetControllerReference(store, &job, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, &job); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("create probe job: %w", err)
		}
		store.Status.ProbeJob = job.Name
		store.Status.ProbedHash = hash
		if meta := findCondition(store.Status.Conditions, odoov1.ConditionReady); meta == nil {
			r.setReady(store, false, ReasonStoreProbing, "job "+job.Name+" is probing the store")
		}
	}

	od := &odoov1.OdooDeployment{}
	od.Namespace = store.Namespace
	od.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{Name: store.Status.ProbeJob, Namespace: store.Namespace}
	obs, err := reconcileloops.ObserveMaintenanceJob(ctx, r.Client, r.reader(), od)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch obs.State {
	case reconcileloops.JobStateMissing:
		store.Status.ProbeJob = ""
		store.Status.ProbedHash = ""
		return ctrl.Result{Requeue: true}, nil
	case reconcileloops.JobStateActive:
		if obs.ImagePullMessage != "" {
			r.setReady(store, store.Status.Ready, ReasonStoreProbing, "probe job "+obs.Job.Name+" cannot start: "+obs.ImagePullMessage)
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	now := metav1.NewTime(r.now())
	store.Status.LastProbeTime = &now
	out, err := (&OdooDeploymentReconciler{Client: r.Client, APIReader: r.APIReader}).observeJob(ctx, store.Namespace, obs.Job.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if obs.State == reconcileloops.JobStateSucceeded && out.decoded && out.result.OK {
		r.setReady(store, true, ReasonStoreReachable, "the store can be written to, read from and deleted from")
		if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, obs.Job); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		msg := fmt.Sprintf("probe %s; inspect with: kubectl -n %s logs job/%s", failureText(out), store.Namespace, obs.Job.Name)
		if store.Status.Ready || findCondition(store.Status.Conditions, odoov1.ConditionReady) == nil ||
			findCondition(store.Status.Conditions, odoov1.ConditionReady).Message != msg {
			if r.Recorder != nil {
				r.Recorder.Event(store, corev1.EventTypeWarning, ReasonStoreUnreachable, msg)
			}
		}
		r.setReady(store, false, ReasonStoreUnreachable, msg)
	}
	store.Status.ProbeJob = ""
	log.FromContext(ctx).Info("Probed backup store", "store", store.Name, "ready", store.Status.Ready)
	return ctrl.Result{RequeueAfter: nextPeriod}, nil
}

func findCondition(conds []metav1.Condition, t string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == t {
			return &conds[i]
		}
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *OdooBackupStoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&odoov1.OdooBackupStore{}).
		Owns(&batchv1.Job{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.storesUsingSecret)).
		Named("odoobackupstore").
		Complete(r)
}

// storesUsingSecret maps a Secret to the stores that read it.
func (r *OdooBackupStoreReconciler) storesUsingSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	stores := &odoov1.OdooBackupStoreList{}
	if err := r.List(ctx, stores, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range stores.Items {
		if _, ok := jobspec.StoreSecrets(&stores.Items[i])[obj.GetName()]; ok {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&stores.Items[i])})
		}
	}
	return reqs
}
