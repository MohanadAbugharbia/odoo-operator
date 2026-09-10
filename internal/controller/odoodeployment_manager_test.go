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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/database"
)

// Everything else in this suite calls Reconcile directly, which proves the
// reconcile logic but not that anything would ever call it. These specs run a
// real manager built by SetupWithManager and only ever touch cluster objects,
// so a reconcile happening at all is the assertion: it means the watch, its
// predicate and its mapper all did their job.
var _ = Describe("Manager wiring", Ordered, func() {
	const (
		managerNS = "watch-wiring"
		odName    = "wired"
		dbSecret  = "wired-db"
	)
	configKey := types.NamespacedName{Name: odName + "-config", Namespace: managerNS}

	// The manager's cache is scoped to managerNS so this running controller
	// cannot observe (or fight over) the objects the other specs create in
	// "default".
	BeforeAll(func() {
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: managerNS},
		})).To(Succeed())

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:  k8sClient.Scheme(),
			Metrics: metricsserver.Options{BindAddress: "0"},
			Cache: cache.Options{
				DefaultNamespaces: map[string]cache.Config{managerNS: {}},
			},
		})
		Expect(err).NotTo(HaveOccurred())

		recorder := record.NewFakeRecorder(200)
		go func() {
			for range recorder.Events {
			}
		}()

		reconciler := &OdooDeploymentReconciler{
			Client:    mgr.GetClient(),
			Scheme:    mgr.GetScheme(),
			APIReader: mgr.GetAPIReader(),
			DB:        database.NewFake(),
			Recorder:  recorder,
		}
		Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

		managerCtx, stopManager := context.WithCancel(ctx)
		stopped := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(stopped)
			Expect(mgr.Start(managerCtx)).To(Succeed())
		}()
		DeferCleanup(func() {
			stopManager()
			Eventually(stopped, 30*time.Second).Should(BeClosed())
		})
		Expect(mgr.GetCache().WaitForCacheSync(managerCtx)).To(BeTrue())

		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: dbSecret, Namespace: managerNS},
			Data:       map[string][]byte{"password": []byte("first-password")},
		})).To(Succeed())
	})

	// odooConf returns the rendered configuration the operator last wrote.
	odooConf := func() string {
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, configKey, secret); err != nil {
			return ""
		}
		return string(secret.Data["odoo.conf"])
	}

	It("reconciles a new OdooDeployment without anyone calling Reconcile", func() {
		od := &odoov1.OdooDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: odName, Namespace: managerNS},
			Spec: odoov1.OdooDeploymentSpec{
				Name:  odName,
				Image: "odoo:18",
				Database: odoov1.OdooDatabaseConfig{
					Host: "db-host",
					Port: 5432,
					User: "odoo",
					Name: "wired_db",
					PasswordFromSecret: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: dbSecret},
						Key:                  "password",
					},
				},
				Modules: []string{"base"},
				OdooFilestore: odoov1.PersistentVolumeClaimSpec{
					Size: resource.MustParse("1Gi"),
				},
			},
		}
		Expect(k8sClient.Create(ctx, od)).To(Succeed())

		Eventually(odooConf, 30*time.Second).Should(ContainSubstring("db_password = first-password"))
	})

	It("restores an owned child Secret that was edited out from under it", func() {
		// The config Secret is owned by the OdooDeployment, so this exercises
		// the Owns() watch: nothing references it by name.
		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, configKey, secret)).To(Succeed())
		secret.Data = map[string][]byte{"odoo.conf": []byte("[options]\ntampered = yes\n")}
		Expect(k8sClient.Update(ctx, secret)).To(Succeed())

		Eventually(odooConf, 30*time.Second).Should(ContainSubstring("db_password = first-password"))
		Expect(odooConf()).NotTo(ContainSubstring("tampered"))
	})

	It("re-renders when a referenced Secret it does not own changes", func() {
		// The database credential Secret is referenced by name and owned by
		// nobody, so only the mapper can connect it back to the OdooDeployment.
		// Its data changes, so the predicate lets the event through.
		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dbSecret, Namespace: managerNS}, secret)).To(Succeed())
		secret.Data = map[string][]byte{"password": []byte("second-password")}
		Expect(k8sClient.Update(ctx, secret)).To(Succeed())

		Eventually(odooConf, 30*time.Second).Should(ContainSubstring("db_password = second-password"))
	})

	It("ignores an update to a referenced Secret that does not change the data", func() {
		// The predicate drops this event, so the rendered configuration must
		// stay byte-for-byte identical. A label edit bumps the
		// resourceVersion, which is exactly what the filter exists to absorb.
		before := odooConf()
		Expect(before).NotTo(BeEmpty())

		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dbSecret, Namespace: managerNS}, secret)).To(Succeed())
		metav1.SetMetaDataLabel(&secret.ObjectMeta, "touched", "yes")
		Expect(k8sClient.Update(ctx, secret)).To(Succeed())

		Consistently(odooConf, 3*time.Second).Should(Equal(before))
		Expect(strings.Count(before, "db_password")).To(Equal(1))
	})
})
