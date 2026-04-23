package reconcileloops

import (
	"context"
	"fmt"
	"sync/atomic"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// specCounter generates unique resource names per test so that lingering
// objects from one test (envtest has no GC) never collide with the next.
var specCounter int64

var _ = Describe("DatabaseInitJob Reconcile Loop", func() {
	var (
		reconciler         *OdooDatabaseInitJobReconciler
		ctx                context.Context
		req                reconcile.Request
		odooDeployment     *odoov1.OdooDeployment
		resourceName       string
		typeNamespacedName types.NamespacedName
	)

	const resourceNamespace = "default"

	ctx = context.Background()

	newOdooDeployment := func(name string, specModules []string) *odoov1.OdooDeployment {
		return &odoov1.OdooDeployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: resourceNamespace,
			},
			Spec: odoov1.OdooDeploymentSpec{
				Replicas: 1,
				Image:    "mohanadabugharbia/odoo:18",
				OdooFilestore: odoov1.PersistentVolumeClaimSpec{
					Size: resource.MustParse("1Gi"),
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
				},
				Database: odoov1.OdooDatabaseConfig{
					HostFromSecret: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-dbinitjob-db-secret"},
						Key:                  "host",
					},
					PortFromSecret: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-dbinitjob-db-secret"},
						Key:                  "port",
					},
					UserFromSecret: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-dbinitjob-db-secret"},
						Key:                  "user",
					},
					PasswordFromSecret: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-dbinitjob-db-secret"},
						Key:                  "password",
					},
					NameFromSecret: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-dbinitjob-db-secret"},
						Key:                  "name",
					},
					SSLFromSecret: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-dbinitjob-db-secret"},
						Key:                  "ssl",
					},
					MaxConnFromSecret: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-dbinitjob-db-secret"},
						Key:                  "maxconn",
					},
				},
				Modules: specModules,
				Config: odoov1.OdooConfig{
					DataDir: "/var/lib/odoo",
				},
			},
		}
	}

	// tryDelete issues a Delete and tolerates "not found" — used in AfterEach
	// where the reconciler may have already deleted the object.
	tryDelete := func(obj client.Object) {
		err := k8sClient.Delete(ctx, obj)
		if err != nil && !errors.IsNotFound(err) {
			Expect(err).NotTo(HaveOccurred())
		}
	}

	BeforeEach(func() {
		n := atomic.AddInt64(&specCounter, 1)
		resourceName = fmt.Sprintf("test-dij-%d", n)
		typeNamespacedName = types.NamespacedName{Name: resourceName, Namespace: resourceNamespace}

		By("creating the OdooDeployment CR")
		odooDeployment = newOdooDeployment(resourceName, []string{"base", "web"})
		Expect(k8sClient.Create(ctx, odooDeployment)).To(Succeed())

		By("creating the PVC for the OdooDeployment")
		pvc := odooDeployment.GetPvcTemplate()
		Expect(k8sClient.Create(ctx, &pvc)).To(Succeed())
		odooDeployment.Status.OdooDataPvcName = pvc.Name
		Expect(k8sClient.Status().Update(ctx, odooDeployment)).To(Succeed())

		By("creating the config secret for the OdooDeployment")
		configSecret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      odooDeployment.Name + "-config",
				Namespace: resourceNamespace,
			},
			Data: map[string][]byte{"odoo.conf": []byte("[options]")},
		}
		Expect(k8sClient.Create(ctx, &configSecret)).To(Succeed())
		odooDeployment.Status.OdooConfigSecretName = configSecret.Name
		Expect(k8sClient.Status().Update(ctx, odooDeployment)).To(Succeed())

		reconciler = &OdooDatabaseInitJobReconciler{
			Client:         k8sClient,
			Scheme:         k8sClient.Scheme(),
			OdooDeployment: odooDeployment,
		}
		req = ctrl.Request{
			NamespacedName: client.ObjectKey{Name: resourceName, Namespace: resourceNamespace},
		}
	})

	AfterEach(func() {
		By("deleting the init job if it exists")
		job := &batchv1.Job{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: resourceName + "-init", Namespace: resourceNamespace}, job); err == nil {
			tryDelete(job)
		}

		By("deleting the config secret")
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: resourceName + "-config", Namespace: resourceNamespace}, secret); err == nil {
			tryDelete(secret)
		}

		By("deleting the PVC")
		pvc := odooDeployment.GetPvcTemplate()
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}, &pvc); err == nil {
			tryDelete(&pvc)
		}

		By("deleting the OdooDeployment CR")
		od := &odoov1.OdooDeployment{}
		if err := k8sClient.Get(ctx, typeNamespacedName, od); err == nil {
			tryDelete(od)
		}
	})

	// Scenario A: no modules installed → job created with all spec modules
	It("A: creates an InitJob with all spec modules when nothing is installed", func() {
		_, _, requeue := reconciler.Reconcile(ctx, req)

		Expect(requeue).To(BeTrue())
		Expect(odooDeployment.Status.CurrentInitJob.Modules).To(ConsistOf("base", "web"))

		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      resourceName + "-init",
			Namespace: resourceNamespace,
		}, job)).To(Succeed())
	})

	// Scenario B: some modules already installed → job contains only the delta
	It("B: creates an InitJob with only new modules when some are already installed", func() {
		odooDeployment.Status.InitModulesInstalled = []string{"base"}
		Expect(k8sClient.Status().Update(ctx, odooDeployment)).To(Succeed())

		reconciler.OdooDeployment = odooDeployment
		_, _, requeue := reconciler.Reconcile(ctx, req)

		Expect(requeue).To(BeTrue())
		Expect(odooDeployment.Status.CurrentInitJob.Modules).To(ConsistOf("web"))

		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      resourceName + "-init",
			Namespace: resourceNamespace,
		}, job)).To(Succeed())
	})

	// Scenario B': same-length module replacement — e.g. ["base","web"] → ["base","crm"]
	It("B': creates an InitJob when spec modules changed to different modules with same count", func() {
		odooDeployment.Status.InitModulesInstalled = []string{"base", "web"}
		Expect(k8sClient.Status().Update(ctx, odooDeployment)).To(Succeed())

		odooDeployment.Spec.Modules = []string{"base", "crm"}
		Expect(k8sClient.Update(ctx, odooDeployment)).To(Succeed())

		reconciler.OdooDeployment = odooDeployment
		_, _, requeue := reconciler.Reconcile(ctx, req)

		Expect(requeue).To(BeTrue())
		Expect(odooDeployment.Status.CurrentInitJob.Modules).To(ConsistOf("crm"))
	})

	// Scenario C: all spec modules already installed → no job, no requeue
	It("C: does not create an InitJob when all modules are already installed", func() {
		odooDeployment.Status.InitModulesInstalled = []string{"base", "web"}
		Expect(k8sClient.Status().Update(ctx, odooDeployment)).To(Succeed())

		reconciler.OdooDeployment = odooDeployment
		_, _, requeue := reconciler.Reconcile(ctx, req)

		Expect(requeue).To(BeFalse())

		job := &batchv1.Job{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      resourceName + "-init",
			Namespace: resourceNamespace,
		}, job)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	// Scenario D: duplicate modules in spec → dedup applied, job has unique modules only
	It("D: deduplicates spec modules before creating the InitJob", func() {
		odooDeployment.Spec.Modules = []string{"base", "web", "base", "web"}
		Expect(k8sClient.Update(ctx, odooDeployment)).To(Succeed())
		odooDeployment.DeduplicateModules()

		reconciler.OdooDeployment = odooDeployment
		_, _, requeue := reconciler.Reconcile(ctx, req)

		Expect(requeue).To(BeTrue())
		Expect(odooDeployment.Status.CurrentInitJob.Modules).To(ConsistOf("base", "web"))
		Expect(odooDeployment.Status.CurrentInitJob.Modules).To(HaveLen(2))
	})

	// Scenario E: job succeeded → InitModulesInstalled grows by append, not replace
	It("E: appends modules to InitModulesInstalled when job succeeds", func() {
		odooDeployment.Status.InitModulesInstalled = []string{"base"}
		odooDeployment.Status.CurrentInitJob = odoov1.DBInitjob{
			Name:      resourceName + "-init",
			Namespace: resourceNamespace,
			Modules:   []string{"web"},
		}
		Expect(k8sClient.Status().Update(ctx, odooDeployment)).To(Succeed())

		succeededJob := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      resourceName + "-init",
				Namespace: resourceNamespace,
			},
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers:    []corev1.Container{{Name: "init", Image: "odoo:18"}},
						RestartPolicy: corev1.RestartPolicyNever,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, succeededJob)).To(Succeed())
		succeededJob.Status.Succeeded = 1
		Expect(k8sClient.Status().Update(ctx, succeededJob)).To(Succeed())

		reconciler.OdooDeployment = odooDeployment
		_, _, requeue := reconciler.Reconcile(ctx, req)

		Expect(requeue).To(BeTrue())
		Expect(odooDeployment.Status.InitModulesInstalled).To(ConsistOf("base", "web"))
		Expect(odooDeployment.Status.CurrentInitJob.Name).To(BeEmpty())
		Expect(odooDeployment.Status.CurrentInitJob.Modules).To(BeEmpty())
	})
})
