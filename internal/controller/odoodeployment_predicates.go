package controller

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

var (
	isUsefulOdooDeploymentSecret = func(object client.Object) bool {
		return isOwnedByOdooDeploymentOrSatisfiesPredicate(object, func(object client.Object) bool {
			_, ok := object.(*corev1.Secret)
			return ok
		})
	}

	isUsefulOdooDeploymentDeployment = func(object client.Object) bool {
		return isOwnedByOdooDeploymentOrSatisfiesPredicate(object, func(object client.Object) bool {
			_, ok := object.(*appsv1.Deployment)
			return ok
		})
	}

	isUsefulOdooDeploymentService = func(object client.Object) bool {
		return isOwnedByOdooDeploymentOrSatisfiesPredicate(object, func(object client.Object) bool {
			_, ok := object.(*corev1.Service)
			return ok
		})
	}

	isUsefulOdooDeploymentPVC = func(object client.Object) bool {
		return isOwnedByOdooDeploymentOrSatisfiesPredicate(object, func(object client.Object) bool {
			_, ok := object.(*corev1.PersistentVolumeClaim)
			return ok
		})
	}
	secretsPredicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			result := isUsefulOdooDeploymentSecret(e.Object)
			if result {
				ctrllog.Log.V(1).Info("Secret created, triggering reconcile",
					"secret", e.Object.GetName(),
					"namespace", e.Object.GetNamespace())
			}
			return result
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			result := isUsefulOdooDeploymentSecret(e.Object)
			if result {
				ctrllog.Log.V(1).Info("Secret deleted, triggering reconcile",
					"secret", e.Object.GetName(),
					"namespace", e.Object.GetNamespace())
			}
			return result
		},
		GenericFunc: func(e event.GenericEvent) bool {
			if _, owned := IsOwnedByOdooDeployment(e.Object); owned {
				return true
			}
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			if !isUsefulOdooDeploymentSecret(e.ObjectNew) {
				return false
			}
			// Only trigger reconcile if secret data actually changed
			oldSecret, oldOk := e.ObjectOld.(*corev1.Secret)
			newSecret, newOk := e.ObjectNew.(*corev1.Secret)
			if oldOk && newOk {
				// Compare secret data - only reconcile if data changed
				dataChanged := !equalSecretData(oldSecret.Data, newSecret.Data)
				if dataChanged {
					ctrllog.Log.V(1).Info("Secret data changed, triggering reconcile",
						"secret", newSecret.Name,
						"namespace", newSecret.Namespace,
						"oldResourceVersion", oldSecret.ResourceVersion,
						"newResourceVersion", newSecret.ResourceVersion)
				} else {
					ctrllog.Log.V(1).Info("Secret updated but data unchanged, skipping reconcile",
						"secret", newSecret.Name,
						"namespace", newSecret.Namespace)
				}
				return dataChanged
			}
			return true
		},
	}

	// deploymentPredicate filters deployment events
	deploymentPredicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			result := isUsefulOdooDeploymentDeployment(e.Object)
			if result {
				ctrllog.Log.V(1).Info("Deployment created, triggering reconcile",
					"deployment", e.Object.GetName(),
					"namespace", e.Object.GetNamespace())
			}
			return result
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			result := isUsefulOdooDeploymentDeployment(e.ObjectNew)
			if result {
				ctrllog.Log.V(1).Info("Deployment updated, triggering reconcile",
					"deployment", e.ObjectNew.GetName(),
					"namespace", e.ObjectNew.GetNamespace())
			}
			return result
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			result := isUsefulOdooDeploymentDeployment(e.Object)
			if result {
				ctrllog.Log.V(1).Info("Deployment deleted, triggering reconcile",
					"deployment", e.Object.GetName(),
					"namespace", e.Object.GetNamespace())
				return true
			}
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			if _, owned := IsOwnedByOdooDeployment(e.Object); owned {
				return true
			}
			return false
		},
	}

	// servicePredicate filters service events
	servicePredicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			result := isUsefulOdooDeploymentService(e.Object)
			if result {
				ctrllog.Log.V(1).Info("Service created, triggering reconcile",
					"service", e.Object.GetName(),
					"namespace", e.Object.GetNamespace())
			}
			return result
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			result := isUsefulOdooDeploymentService(e.ObjectNew)
			if result {
				ctrllog.Log.V(1).Info("Service updated, triggering reconcile",
					"service", e.ObjectNew.GetName(),
					"namespace", e.ObjectNew.GetNamespace())
			}
			return result
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			result := isUsefulOdooDeploymentService(e.Object)
			if result {
				ctrllog.Log.V(1).Info("Service deleted, triggering reconcile",
					"service", e.Object.GetName(),
					"namespace", e.Object.GetNamespace())
			}
			return result
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return isUsefulOdooDeploymentService(e.Object)
		},
	}

	// pvcPredicate filters PVC events
	pvcPredicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			result := isUsefulOdooDeploymentPVC(e.Object)
			if result {
				ctrllog.Log.V(1).Info("PVC created, triggering reconcile",
					"pvc", e.Object.GetName(),
					"namespace", e.Object.GetNamespace())
			}
			return result
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			result := isUsefulOdooDeploymentPVC(e.ObjectNew)
			if result {
				ctrllog.Log.V(1).Info("PVC updated, triggering reconcile",
					"pvc", e.ObjectNew.GetName(),
					"namespace", e.ObjectNew.GetNamespace())
			}
			return result
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			result := isUsefulOdooDeploymentPVC(e.Object)
			if result {
				ctrllog.Log.V(1).Info("PVC deleted, triggering reconcile",
					"pvc", e.Object.GetName(),
					"namespace", e.Object.GetNamespace())
			}
			return result
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return isUsefulOdooDeploymentPVC(e.Object)
		},
	}
)

func isOwnedByOdooDeploymentOrSatisfiesPredicate(
	object client.Object,
	predicate func(client.Object) bool,
) bool {
	_, owned := IsOwnedByOdooDeployment(object)
	return owned || predicate(object)
}

// equalSecretData compares two secret data maps
func equalSecretData(old, new map[string][]byte) bool {
	if len(old) != len(new) {
		return false
	}
	for key, oldVal := range old {
		newVal, exists := new[key]
		if !exists {
			return false
		}
		if string(oldVal) != string(newVal) {
			return false
		}
	}
	return true
}
