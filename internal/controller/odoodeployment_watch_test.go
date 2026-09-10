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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// The watch layer decides which cluster events wake the operator: the
// predicates below drop events, the mappers translate a surviving child event
// into the OdooDeployments that own or reference it. Nothing else in the suite
// exercises them, because the lifecycle tests call Reconcile directly.

func watchScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(s))
	utilruntime.Must(appsv1.AddToScheme(s))
	utilruntime.Must(odoov1.AddToScheme(s))
	return s
}

// ownerName is the OdooDeployment the fixtures below are owned by.
const ownerName = "ababiel"

// ownedBy returns the ownerReference the operator stamps on its children.
func ownedBy() metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{
		APIVersion: odoov1.GroupVersion.String(),
		Kind:       odoov1.OdooDeploymentKind,
		Name:       ownerName,
		Controller: &controller,
	}
}

func secret(name string, data map[string][]byte, owners ...metav1.OwnerReference) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "watch-ns", OwnerReferences: owners},
		Data:       data,
	}
}

func TestIsOwnedByOdooDeployment(t *testing.T) {
	controller := true
	tests := []struct {
		name      string
		owner     *metav1.OwnerReference
		wantName  string
		wantOwned bool
	}{
		{name: "no owner reference at all", owner: nil},
		{
			name: "owned by an OdooDeployment",
			owner: &metav1.OwnerReference{
				APIVersion: odoov1.GroupVersion.String(),
				Kind:       odoov1.OdooDeploymentKind,
				Name:       "ababiel",
				Controller: &controller,
			},
			wantName:  "ababiel",
			wantOwned: true,
		},
		{
			name: "another kind in our own group is not ours",
			owner: &metav1.OwnerReference{
				APIVersion: odoov1.GroupVersion.String(),
				Kind:       "SomethingElse",
				Name:       "ababiel",
				Controller: &controller,
			},
		},
		{
			name: "same kind from a foreign group is not ours",
			owner: &metav1.OwnerReference{
				APIVersion: "other.example.com/v1",
				Kind:       odoov1.OdooDeploymentKind,
				Name:       "ababiel",
				Controller: &controller,
			},
		},
		{
			name: "a non-controller owner reference does not count",
			owner: &metav1.OwnerReference{
				APIVersion: odoov1.GroupVersion.String(),
				Kind:       odoov1.OdooDeploymentKind,
				Name:       "ababiel",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "child"}}
			if tc.owner != nil {
				obj.OwnerReferences = []metav1.OwnerReference{*tc.owner}
			}
			gotName, gotOwned := IsOwnedByOdooDeployment(obj)
			if gotOwned != tc.wantOwned || gotName != tc.wantName {
				t.Errorf("IsOwnedByOdooDeployment() = (%q, %v), want (%q, %v)",
					gotName, gotOwned, tc.wantName, tc.wantOwned)
			}
		})
	}
}

func TestEqualSecretData(t *testing.T) {
	tests := []struct {
		name     string
		previous map[string][]byte
		current  map[string][]byte
		want     bool
	}{
		{name: "both empty", previous: map[string][]byte{}, current: map[string][]byte{}, want: true},
		{
			name:     "same keys and values",
			previous: map[string][]byte{"password": []byte("a"), "host": []byte("db")},
			current:  map[string][]byte{"host": []byte("db"), "password": []byte("a")},
			want:     true,
		},
		{
			name:     "a value changed",
			previous: map[string][]byte{"password": []byte("a")},
			current:  map[string][]byte{"password": []byte("b")},
		},
		{
			name:     "a key was added",
			previous: map[string][]byte{"password": []byte("a")},
			current:  map[string][]byte{"password": []byte("a"), "host": []byte("db")},
		},
		{
			name:     "a key was renamed, same length",
			previous: map[string][]byte{"password": []byte("a")},
			current:  map[string][]byte{"secret": []byte("a")},
		},
		{
			name:     "an empty value is not the same as a missing key",
			previous: map[string][]byte{"password": {}},
			current:  map[string][]byte{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := equalSecretData(tc.previous, tc.current); got != tc.want {
				t.Errorf("equalSecretData() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsOwnedByOdooDeploymentOrSatisfiesPredicate(t *testing.T) {
	isSecret := func(o client.Object) bool {
		_, ok := o.(*corev1.Secret)
		return ok
	}

	t.Run("an owned object passes even when the predicate rejects it", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "d", OwnerReferences: []metav1.OwnerReference{ownedBy()}},
		}
		if !isOwnedByOdooDeploymentOrSatisfiesPredicate(deployment, isSecret) {
			t.Error("owned Deployment should pass a Secret-only predicate")
		}
	})

	t.Run("an unowned object passes on the predicate alone", func(t *testing.T) {
		if !isOwnedByOdooDeploymentOrSatisfiesPredicate(secret("free", nil), isSecret) {
			t.Error("any Secret should pass the Secret predicate")
		}
	})

	t.Run("neither owned nor matching is rejected", func(t *testing.T) {
		deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "d"}}
		if isOwnedByOdooDeploymentOrSatisfiesPredicate(deployment, isSecret) {
			t.Error("an unowned Deployment should not pass a Secret-only predicate")
		}
	})
}

// The Secret predicate is the only one that inspects the payload: an update
// that does not change the data must not wake the operator, or every
// resourceVersion bump would roll the pods.
func TestSecretsPredicate(t *testing.T) {
	owned := secret("owned", map[string][]byte{"password": []byte("a")}, ownedBy())
	free := secret("free", map[string][]byte{"password": []byte("a")})
	changed := secret("free", map[string][]byte{"password": []byte("b")})
	notASecret := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "d"}}

	t.Run("create", func(t *testing.T) {
		if !secretsPredicate.Create(event.CreateEvent{Object: free}) {
			t.Error("a new Secret should be delivered")
		}
		if secretsPredicate.Create(event.CreateEvent{Object: notASecret}) {
			t.Error("an unowned non-Secret should be dropped")
		}
	})

	t.Run("delete", func(t *testing.T) {
		if !secretsPredicate.Delete(event.DeleteEvent{Object: free}) {
			t.Error("a deleted Secret should be delivered")
		}
		if secretsPredicate.Delete(event.DeleteEvent{Object: notASecret}) {
			t.Error("an unowned non-Secret should be dropped")
		}
	})

	t.Run("update with unchanged data is dropped", func(t *testing.T) {
		older := free.DeepCopy()
		older.ResourceVersion = "1"
		newer := free.DeepCopy()
		newer.ResourceVersion = "2"
		if secretsPredicate.Update(event.UpdateEvent{ObjectOld: older, ObjectNew: newer}) {
			t.Error("a Secret update that did not touch the data should be dropped")
		}
	})

	t.Run("update with changed data is delivered", func(t *testing.T) {
		if !secretsPredicate.Update(event.UpdateEvent{ObjectOld: free, ObjectNew: changed}) {
			t.Error("a Secret whose data changed should be delivered")
		}
	})

	t.Run("update of an unowned non-Secret is dropped", func(t *testing.T) {
		if secretsPredicate.Update(event.UpdateEvent{ObjectOld: notASecret, ObjectNew: notASecret}) {
			t.Error("an unowned non-Secret update should be dropped")
		}
	})

	t.Run("update is delivered when the pair cannot be compared", func(t *testing.T) {
		// ObjectNew is owned so it passes the filter, but the two objects are
		// not both Secrets, so the data comparison cannot run and the event is
		// delivered rather than silently dropped.
		ownedDeployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "d", OwnerReferences: []metav1.OwnerReference{ownedBy()}},
		}
		if !secretsPredicate.Update(event.UpdateEvent{ObjectOld: free, ObjectNew: ownedDeployment}) {
			t.Error("an uncomparable pair should be delivered")
		}
	})

	t.Run("generic events are restricted to owned Secrets", func(t *testing.T) {
		if !secretsPredicate.Generic(event.GenericEvent{Object: owned}) {
			t.Error("an owned Secret should be delivered")
		}
		if secretsPredicate.Generic(event.GenericEvent{Object: free}) {
			t.Error("generic events are ownership-only, an unowned Secret should be dropped")
		}
	})
}

func TestChildPredicates(t *testing.T) {
	ownedDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "d", OwnerReferences: []metav1.OwnerReference{ownedBy()}},
	}
	tests := []struct {
		name string
		// the predicate under test and an object of the kind it watches
		predicateCreate  func(client.Object) bool
		predicateUpdate  func(client.Object) bool
		predicateDelete  func(client.Object) bool
		predicateGeneric func(client.Object) bool
		match            client.Object
		// wantGeneric records whether a generic event on an *unowned* object of
		// the watched kind is delivered: the Deployment predicate is
		// ownership-only, the Service and PVC ones are not.
		wantGeneric bool
	}{
		{
			name:             "deployment",
			predicateCreate:  func(o client.Object) bool { return deploymentPredicate.Create(event.CreateEvent{Object: o}) },
			predicateUpdate:  func(o client.Object) bool { return deploymentPredicate.Update(event.UpdateEvent{ObjectNew: o}) },
			predicateDelete:  func(o client.Object) bool { return deploymentPredicate.Delete(event.DeleteEvent{Object: o}) },
			predicateGeneric: func(o client.Object) bool { return deploymentPredicate.Generic(event.GenericEvent{Object: o}) },
			match:            &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "d"}},
			wantGeneric:      false,
		},
		{
			name:             "service",
			predicateCreate:  func(o client.Object) bool { return servicePredicate.Create(event.CreateEvent{Object: o}) },
			predicateUpdate:  func(o client.Object) bool { return servicePredicate.Update(event.UpdateEvent{ObjectNew: o}) },
			predicateDelete:  func(o client.Object) bool { return servicePredicate.Delete(event.DeleteEvent{Object: o}) },
			predicateGeneric: func(o client.Object) bool { return servicePredicate.Generic(event.GenericEvent{Object: o}) },
			match:            &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "s"}},
			wantGeneric:      true,
		},
		{
			name:             "pvc",
			predicateCreate:  func(o client.Object) bool { return pvcPredicate.Create(event.CreateEvent{Object: o}) },
			predicateUpdate:  func(o client.Object) bool { return pvcPredicate.Update(event.UpdateEvent{ObjectNew: o}) },
			predicateDelete:  func(o client.Object) bool { return pvcPredicate.Delete(event.DeleteEvent{Object: o}) },
			predicateGeneric: func(o client.Object) bool { return pvcPredicate.Generic(event.GenericEvent{Object: o}) },
			match:            &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "p"}},
			wantGeneric:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for verb, run := range map[string]func(client.Object) bool{
				"create": tc.predicateCreate,
				"update": tc.predicateUpdate,
				"delete": tc.predicateDelete,
			} {
				if !run(tc.match) {
					t.Errorf("%s of a watched %s should be delivered", verb, tc.name)
				}
				// A Secret is never of the watched kind here, and unowned.
				if run(secret("unrelated", nil)) {
					t.Errorf("%s of an unrelated unowned object should be dropped", verb)
				}
			}
			if got := tc.predicateGeneric(tc.match); got != tc.wantGeneric {
				t.Errorf("generic of an unowned %s = %v, want %v", tc.name, got, tc.wantGeneric)
			}
			if !tc.predicateGeneric(ownedDeployment) && tc.name == "deployment" {
				t.Error("generic of an owned Deployment should be delivered")
			}
		})
	}
}

// mapperFixture builds three OdooDeployments: one that references the child
// under test, one in the same namespace that does not, and one in another
// namespace that does. A mapper must return exactly the first.
func mapperFixture(t *testing.T, mutate func(od *odoov1.OdooDeployment, referencing bool)) *OdooDeploymentReconciler {
	t.Helper()
	build := func(name, namespace string, referencing bool) *odoov1.OdooDeployment {
		od := &odoov1.OdooDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: odoov1.OdooDeploymentSpec{
				Name:  name,
				Image: "odoo:18",
				Database: odoov1.OdooDatabaseConfig{
					Host: "db", Port: 5432, User: "odoo", Name: "odoo",
				},
			},
		}
		mutate(od, referencing)
		return od
	}
	s := watchScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		build("wants-it", "watch-ns", true),
		build("does-not", "watch-ns", false),
		build("other-namespace", "elsewhere", true),
	).Build()
	return &OdooDeploymentReconciler{Client: c, Scheme: s}
}

func wantsItOnly(t *testing.T, requests []reconcile.Request) {
	t.Helper()
	want := []reconcile.Request{
		{NamespacedName: types.NamespacedName{Name: "wants-it", Namespace: "watch-ns"}},
	}
	if len(requests) != len(want) || (len(requests) == 1 && requests[0] != want[0]) {
		t.Errorf("requests = %v, want %v", requests, want)
	}
}

func TestMapSecretsToOdooDeployments(t *testing.T) {
	r := mapperFixture(t, func(od *odoov1.OdooDeployment, referencing bool) {
		name := "unrelated-secret"
		if referencing {
			name = "db-cred"
		}
		od.Spec.Database.PasswordFromSecret = corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: name},
			Key:                  "password",
		}
	})
	ctx := context.Background()

	t.Run("only the referencing OdooDeployment in the same namespace", func(t *testing.T) {
		wantsItOnly(t, r.mapSecretsToOdooDeployments()(ctx, secret("db-cred", nil)))
	})

	t.Run("a Secret nothing references enqueues nothing", func(t *testing.T) {
		if got := r.mapSecretsToOdooDeployments()(ctx, secret("nobody-cares", nil)); len(got) != 0 {
			t.Errorf("requests = %v, want none", got)
		}
	})

	t.Run("an object of another kind is ignored", func(t *testing.T) {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "db-cred", Namespace: "watch-ns"}}
		if got := r.mapSecretsToOdooDeployments()(ctx, cm); got != nil {
			t.Errorf("requests = %v, want nil", got)
		}
	})
}

func TestMapPVCsToOdooDeployments(t *testing.T) {
	r := mapperFixture(t, func(od *odoov1.OdooDeployment, referencing bool) {
		if referencing {
			od.Status.OdooDataPvcName = "filestore"
		}
	})
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "filestore", Namespace: "watch-ns"},
	}
	wantsItOnly(t, r.mapPVCsToOdooDeployments()(context.Background(), pvc))

	if got := r.mapPVCsToOdooDeployments()(context.Background(), secret("filestore", nil)); got != nil {
		t.Errorf("requests = %v, want nil for a non-PVC", got)
	}
}

func TestMapDeploymentsToOdooDeployments(t *testing.T) {
	// UsesDeployment matches on the OdooDeployment's own name.
	r := mapperFixture(t, func(_ *odoov1.OdooDeployment, _ bool) {})
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "wants-it", Namespace: "watch-ns"},
	}
	wantsItOnly(t, r.mapDeploymentsToOdooDeployments()(context.Background(), deployment))

	if got := r.mapDeploymentsToOdooDeployments()(context.Background(), secret("wants-it", nil)); got != nil {
		t.Errorf("requests = %v, want nil for a non-Deployment", got)
	}
}

func TestMapServicesToOdooDeployments(t *testing.T) {
	r := mapperFixture(t, func(_ *odoov1.OdooDeployment, _ bool) {})
	ctx := context.Background()

	for _, suffix := range []string{"-http", "-poll"} {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "wants-it" + suffix, Namespace: "watch-ns"},
		}
		wantsItOnly(t, r.mapServicesToOdooDeployments()(ctx, svc))
	}

	other := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "wants-it-metrics", Namespace: "watch-ns"}}
	if got := r.mapServicesToOdooDeployments()(ctx, other); len(got) != 0 {
		t.Errorf("requests = %v, want none for a Service the operator does not own", got)
	}
	if got := r.mapServicesToOdooDeployments()(ctx, secret("wants-it-http", nil)); got != nil {
		t.Errorf("requests = %v, want nil for a non-Service", got)
	}
}

// Each lookup rejects an object of the wrong kind rather than listing on it.
func TestMapperLookupsRejectTheWrongKind(t *testing.T) {
	r := mapperFixture(t, func(_ *odoov1.OdooDeployment, _ bool) {})
	ctx := context.Background()
	wrong := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}}

	tests := map[string]func() error{
		"secrets or config maps": func() error {
			_, err := r.getOdooDeploymentsForSecretsOrConfigMapsToOdooDeploymentsMapper(ctx, wrong)
			return err
		},
		"pvcs": func() error {
			_, err := r.getOdooDeploymentsForPVCsToOdooDeploymentsMapper(ctx, wrong)
			return err
		},
		"deployments": func() error {
			_, err := r.getOdooDeploymentsForDeploymentsToOdooDeploymentsMapper(ctx, wrong)
			return err
		},
		"services": func() error {
			_, err := r.getOdooDeploymentsForServicesToOdooDeploymentsMapper(ctx, wrong)
			return err
		},
	}
	for name, lookup := range tests {
		t.Run(name, func(t *testing.T) {
			if err := lookup(); err == nil {
				t.Error("expected an error for an unsupported object")
			}
		})
	}

	t.Run("a ConfigMap is a supported source", func(t *testing.T) {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "watch-ns"}}
		list, err := r.getOdooDeploymentsForSecretsOrConfigMapsToOdooDeploymentsMapper(ctx, cm)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(list.Items) != 2 {
			t.Errorf("listed %d OdooDeployments in watch-ns, want 2", len(list.Items))
		}
	})
}
