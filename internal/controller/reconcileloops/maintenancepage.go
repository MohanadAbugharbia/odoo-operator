package reconcileloops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/maintenancepage"
)

// EnsureMaintenancePage reconciles the maintenance page Deployment, running
// image with the given replicas. Its status comes from
// PublishMaintenancePageStatus.
func EnsureMaintenancePage(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	od *odoov1.OdooDeployment,
	image string,
	replicas int32,
) (*appsv1.Deployment, error) {
	desired := od.GetMaintenancePageDeploymentTemplate(image, replicas)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: desired.Name, Namespace: desired.Namespace}}
	result, err := controllerutil.CreateOrUpdate(ctx, c, deployment, func() error {
		if deployment.CreationTimestamp.IsZero() {
			deployment.Spec.Selector = desired.Spec.Selector
		}
		deployment.Labels = mergeMaps(deployment.Labels, desired.Labels)
		deployment.Spec.Replicas = desired.Spec.Replicas
		deployment.Spec.RevisionHistoryLimit = desired.Spec.RevisionHistoryLimit
		deployment.Spec.Template.Labels = mergeMaps(deployment.Spec.Template.Labels, desired.Spec.Template.Labels)
		// The template leaves fields to API-server defaulting; only write it
		// when a field it does set differs, or every reconcile updates it.
		if !equality.Semantic.DeepDerivative(desired.Spec.Template.Spec, deployment.Spec.Template.Spec) {
			deployment.Spec.Template.Spec = desired.Spec.Template.Spec
		}
		return controllerutil.SetControllerReference(od, deployment, scheme)
	})
	if err != nil {
		return nil, fmt.Errorf("maintenance page deployment %s: %w", desired.Name, err)
	}
	if result != controllerutil.OperationResultNone {
		log.FromContext(ctx).Info("Reconciled maintenance page", "deployment", desired.Name, "result", result, "replicas", replicas)
	}
	return deployment, nil
}

// PublishMaintenancePageStatus writes what the page shows into the
// <name>-maintenance ConfigMap the page mounts. The snapshot carries no clock,
// so the ConfigMap only changes when the page has something new to show.
//
// The kubelet refreshes a mounted ConfigMap only when it syncs the pod, which
// it does once a minute on its own. A change to the pod object makes it sync
// at once, so on every change the running page pods get the snapshot's hash
// as an annotation and pick it up within a second or two.
func PublishMaintenancePageStatus(
	ctx context.Context,
	c client.Client,
	reader client.Reader,
	scheme *runtime.Scheme,
	od *odoov1.OdooDeployment,
) error {
	data, err := json.Marshal(maintenancepage.SnapshotOf(od))
	if err != nil {
		return fmt.Errorf("encode maintenance page status: %w", err)
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: od.MaintenancePageName(), Namespace: od.Namespace}}
	result, err := controllerutil.CreateOrUpdate(ctx, c, cm, func() error {
		cm.Labels = mergeMaps(cm.Labels, od.GetMaintenancePageSelectorLabels())
		cm.Data = map[string]string{odoov1.MaintenancePageStatusKey: string(data)}
		return controllerutil.SetControllerReference(od, cm, scheme)
	})
	if err != nil {
		return fmt.Errorf("maintenance page configmap %s: %w", cm.Name, err)
	}
	if result != controllerutil.OperationResultUpdated {
		// Unchanged, or just created: a pod that starts reads it as it is.
		return nil
	}
	sum := sha256.Sum256(data)
	return nudgeMaintenancePods(ctx, c, reader, od, hex.EncodeToString(sum[:])[:16])
}

// nudgeMaintenancePods sets AnnotationMaintenanceStatus on the running page
// pods (best effort: a pod that misses it still refreshes within a minute).
func nudgeMaintenancePods(ctx context.Context, c client.Client, reader client.Reader, od *odoov1.OdooDeployment, hash string) error {
	pods := &corev1.PodList{}
	if err := reader.List(ctx, pods, client.InNamespace(od.Namespace), client.MatchingLabels(od.GetMaintenancePageSelectorLabels())); err != nil {
		return fmt.Errorf("list maintenance page pods: %w", err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !pod.DeletionTimestamp.IsZero() || pod.Annotations[odoov1.AnnotationMaintenanceStatus] == hash {
			continue
		}
		patch := client.MergeFrom(pod.DeepCopy())
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[odoov1.AnnotationMaintenanceStatus] = hash
		if err := c.Patch(ctx, pod, patch); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("annotate maintenance page pod %s: %w", pod.Name, err)
		}
	}
	return nil
}

// RemoveMaintenancePage deletes the maintenance page once
// spec.maintenancePage is turned off. It looks at the Deployment (cached)
// first, so an OdooDeployment that never enabled the page costs no API calls.
func RemoveMaintenancePage(ctx context.Context, c client.Client, od *odoov1.OdooDeployment) error {
	key := client.ObjectKey{Name: od.MaintenancePageName(), Namespace: od.Namespace}
	err := c.Get(ctx, key, &appsv1.Deployment{})
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get maintenance page deployment: %w", err)
	}
	for _, obj := range []client.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}},
	} {
		if err := c.Delete(ctx, obj); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete maintenance page %T: %w", obj, err)
		}
	}
	log.FromContext(ctx).Info("Removed the maintenance page", "name", key.Name)
	return nil
}
