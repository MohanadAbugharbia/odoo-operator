package reconcileloops

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// EnsureFilestorePVC creates the filestore PVC when it is missing. The PVC
// spec is immutable so it is never updated; only the owner reference follows
// spec.odooFilestore.deletionPolicy (Delete → owned and garbage collected,
// Retain → left behind).
func EnsureFilestorePVC(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	od *odoov1.OdooDeployment,
) (*corev1.PersistentVolumeClaim, error) {
	logger := log.FromContext(ctx)
	wantOwned := od.Spec.OdooFilestore.DeletionPolicyValue() == odoov1.DeletionPolicyDelete

	pvc := &corev1.PersistentVolumeClaim{}
	err := c.Get(ctx, client.ObjectKey{Name: od.Name, Namespace: od.Namespace}, pvc)
	if errors.IsNotFound(err) {
		tmpl := od.GetPvcTemplate()
		if wantOwned {
			if err := controllerutil.SetControllerReference(od, &tmpl, scheme); err != nil {
				return nil, err
			}
		}
		logger.Info("Creating filestore PVC", "pvc", tmpl.Name, "owned", wantOwned)
		if err := c.Create(ctx, &tmpl); err != nil {
			return nil, fmt.Errorf("create pvc %s: %w", tmpl.Name, err)
		}
		return &tmpl, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pvc %s: %w", od.Name, err)
	}

	controller := metav1.GetControllerOf(pvc)
	ownedByUs := controller != nil && controller.UID == od.UID
	switch {
	case wantOwned && !ownedByUs:
		patch := client.MergeFrom(pvc.DeepCopy())
		if err := controllerutil.SetControllerReference(od, pvc, scheme); err != nil {
			return nil, fmt.Errorf("own pvc %s: %w", pvc.Name, err)
		}
		logger.Info("Adding owner reference to filestore PVC (deletionPolicy Delete)", "pvc", pvc.Name)
		if err := c.Patch(ctx, pvc, patch); err != nil {
			return nil, fmt.Errorf("patch pvc %s: %w", pvc.Name, err)
		}
	case !wantOwned && ownedByUs:
		patch := client.MergeFrom(pvc.DeepCopy())
		if err := controllerutil.RemoveControllerReference(od, pvc, scheme); err != nil {
			return nil, fmt.Errorf("disown pvc %s: %w", pvc.Name, err)
		}
		logger.Info("Removing owner reference from filestore PVC (deletionPolicy Retain)", "pvc", pvc.Name)
		if err := c.Patch(ctx, pvc, patch); err != nil {
			return nil, fmt.Errorf("patch pvc %s: %w", pvc.Name, err)
		}
	}
	return pvc, nil
}
