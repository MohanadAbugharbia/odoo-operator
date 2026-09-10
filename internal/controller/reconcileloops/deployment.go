package reconcileloops

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

func mergeMaps(dst, src map[string]string) map[string]string {
	if dst == nil {
		dst = make(map[string]string, len(src))
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// EnsureDeployment creates or updates the Odoo Deployment for the given image
// and replica count. Only the fields the operator owns are written, so
// API-server defaults never cause spurious updates; the selector is immutable
// and only set on creation.
func EnsureDeployment(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	od *odoov1.OdooDeployment,
	image string,
	replicas int32,
	configHash string,
) (*appsv1.Deployment, error) {
	logger := log.FromContext(ctx)
	desired := od.GetDeploymentTemplate(image, replicas, configHash)

	deployment := &appsv1.Deployment{}
	deployment.Name = desired.Name
	deployment.Namespace = desired.Namespace

	result, err := controllerutil.CreateOrUpdate(ctx, c, deployment, func() error {
		if deployment.CreationTimestamp.IsZero() {
			deployment.Spec.Selector = desired.Spec.Selector
		}
		deployment.Labels = mergeMaps(deployment.Labels, desired.Labels)
		deployment.Spec.Replicas = desired.Spec.Replicas
		deployment.Spec.Strategy = desired.Spec.Strategy
		deployment.Spec.RevisionHistoryLimit = desired.Spec.RevisionHistoryLimit
		deployment.Spec.ProgressDeadlineSeconds = desired.Spec.ProgressDeadlineSeconds
		deployment.Spec.Template.Labels = mergeMaps(deployment.Spec.Template.Labels, desired.Spec.Template.Labels)
		deployment.Spec.Template.Annotations = mergeMaps(deployment.Spec.Template.Annotations, desired.Spec.Template.Annotations)
		deployment.Spec.Template.Spec = desired.Spec.Template.Spec
		return controllerutil.SetControllerReference(od, deployment, scheme)
	})
	if err != nil {
		return nil, fmt.Errorf("deployment %s: %w", desired.Name, err)
	}
	if result != controllerutil.OperationResultNone {
		logger.Info("Reconciled deployment", "deployment", desired.Name, "result", result, "image", image, "replicas", replicas)
	}
	return deployment, nil
}
