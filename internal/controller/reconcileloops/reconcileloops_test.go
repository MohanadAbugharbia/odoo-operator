package reconcileloops

import (
	"context"
	"fmt"
	"sync/atomic"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/database"
)

// specCounter generates unique resource names per test so that lingering
// objects from one test (envtest has no GC) never collide with the next.
var specCounter int64

const resourceNamespace = "default"

func newOdooDeployment(name string, specModules []string) *odoov1.OdooDeployment {
	replicas := int32(2)
	return &odoov1.OdooDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: resourceNamespace,
		},
		Spec: odoov1.OdooDeploymentSpec{
			Name:        name,
			Replicas:    &replicas,
			Image:       "mohanadabugharbia/odoo:18",
			OdooCommand: []string{"odoo"},
			OdooFilestore: odoov1.PersistentVolumeClaimSpec{
				Size:             resource.MustParse("1Gi"),
				StorageClassName: "standard",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			},
			Database: odoov1.OdooDatabaseConfig{
				Host: "db-host",
				Port: 5432,
				User: "odoo",
				Name: "odoo",
				PasswordFromSecret: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: name + "-db"},
					Key:                  "password",
				},
			},
			Modules: specModules,
			Config: odoov1.OdooConfig{
				DataDir: "/var/lib/odoo",
			},
		},
	}
}

// tryDelete issues a Delete and tolerates "not found".
func tryDelete(obj client.Object) {
	err := k8sClient.Delete(ctx, obj)
	if err != nil && !errors.IsNotFound(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

var _ = Describe("Reconcile loops", func() {
	var (
		od           *odoov1.OdooDeployment
		resourceName string
	)

	BeforeEach(func() {
		n := atomic.AddInt64(&specCounter, 1)
		resourceName = fmt.Sprintf("test-loop-%d", n)

		By("creating the OdooDeployment CR")
		od = newOdooDeployment(resourceName, []string{"base", "web"})
		Expect(k8sClient.Create(ctx, od)).To(Succeed())
	})

	AfterEach(func() {
		for _, obj := range []client.Object{
			&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: resourceName + "-init", Namespace: resourceNamespace}},
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace}},
			&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: resourceName + "-http", Namespace: resourceNamespace}},
			&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: resourceName + "-poll", Namespace: resourceNamespace}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: resourceName + "-config", Namespace: resourceNamespace}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: resourceName + "-admin-password", Namespace: resourceNamespace}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: resourceName + "-custom-admin", Namespace: resourceNamespace}},
			&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace}},
			&odoov1.OdooDeployment{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace}},
		} {
			tryDelete(obj)
		}
	})

	Context("EnsureDeployment", func() {
		It("creates the Deployment with the requested image, replicas and hash, owned by the CR", func() {
			d, err := EnsureDeployment(ctx, k8sClient, k8sClient.Scheme(), od, "img:a", 2, "hash1")
			Expect(err).NotTo(HaveOccurred())
			Expect(d.Name).To(Equal(resourceName))
			Expect(*d.Spec.Replicas).To(Equal(int32(2)))
			Expect(d.Spec.Template.Spec.Containers[0].Image).To(Equal("img:a"))
			Expect(d.Spec.Template.Annotations).To(HaveKeyWithValue(odoov1.AnnotationConfigHash, "hash1"))
			Expect(d.Spec.Strategy.Type).To(Equal(appsv1.RecreateDeploymentStrategyType))
			Expect(d.OwnerReferences).To(HaveLen(1))
			Expect(d.OwnerReferences[0].Kind).To(Equal("OdooDeployment"))
			Expect(*d.OwnerReferences[0].Controller).To(BeTrue())
		})

		It("is a no-op on a second identical reconcile and updates only what changed", func() {
			d1, err := EnsureDeployment(ctx, k8sClient, k8sClient.Scheme(), od, "img:a", 2, "hash1")
			Expect(err).NotTo(HaveOccurred())
			d2, err := EnsureDeployment(ctx, k8sClient, k8sClient.Scheme(), od, "img:a", 2, "hash1")
			Expect(err).NotTo(HaveOccurred())
			Expect(d2.ResourceVersion).To(Equal(d1.ResourceVersion), "API-server defaults must not cause spurious updates")

			d3, err := EnsureDeployment(ctx, k8sClient, k8sClient.Scheme(), od, "img:b", 0, "hash2")
			Expect(err).NotTo(HaveOccurred())
			Expect(*d3.Spec.Replicas).To(BeZero())
			Expect(d3.Spec.Template.Spec.Containers[0].Image).To(Equal("img:b"))
			Expect(d3.Spec.Template.Annotations[odoov1.AnnotationConfigHash]).To(Equal("hash2"))
			Expect(d3.Spec.Selector.MatchLabels).To(Equal(map[string]string{"app": resourceName}))
		})

		It("uses a custom OdooCommand", func() {
			od.Spec.OdooCommand = []string{"/usr/bin/env", "odoo"}
			d, err := EnsureDeployment(ctx, k8sClient, k8sClient.Scheme(), od, "img:a", 1, "h")
			Expect(err).NotTo(HaveOccurred())
			Expect(d.Spec.Template.Spec.Containers[0].Command[:2]).To(Equal([]string{"/usr/bin/env", "odoo"}))
		})
	})

	Context("EnsureFilestorePVC", func() {
		It("creates an unowned PVC with deletionPolicy Retain and follows policy changes", func() {
			pvc, err := EnsureFilestorePVC(ctx, k8sClient, k8sClient.Scheme(), od)
			Expect(err).NotTo(HaveOccurred())
			Expect(pvc.OwnerReferences).To(BeEmpty())
			Expect(pvc.Spec.Resources.Requests.Storage().String()).To(Equal("1Gi"))

			od.Spec.OdooFilestore.DeletionPolicy = odoov1.DeletionPolicyDelete
			pvc, err = EnsureFilestorePVC(ctx, k8sClient, k8sClient.Scheme(), od)
			Expect(err).NotTo(HaveOccurred())
			Expect(pvc.OwnerReferences).To(HaveLen(1))
			Expect(pvc.OwnerReferences[0].UID).To(Equal(od.UID))

			od.Spec.OdooFilestore.DeletionPolicy = odoov1.DeletionPolicyRetain
			pvc, err = EnsureFilestorePVC(ctx, k8sClient, k8sClient.Scheme(), od)
			Expect(err).NotTo(HaveOccurred())
			Expect(pvc.OwnerReferences).To(BeEmpty())
		})

		It("creates an owned PVC with deletionPolicy Delete", func() {
			od.Spec.OdooFilestore.DeletionPolicy = odoov1.DeletionPolicyDelete
			pvc, err := EnsureFilestorePVC(ctx, k8sClient, k8sClient.Scheme(), od)
			Expect(err).NotTo(HaveOccurred())
			Expect(pvc.OwnerReferences).To(HaveLen(1))
		})
	})

	Context("Services", func() {
		It("creates both services owned by the CR and keeps the ClusterIP", func() {
			http, err := EnsureHttpService(ctx, k8sClient, k8sClient.Scheme(), od)
			Expect(err).NotTo(HaveOccurred())
			poll, err := EnsurePollService(ctx, k8sClient, k8sClient.Scheme(), od)
			Expect(err).NotTo(HaveOccurred())
			Expect(http.OwnerReferences).To(HaveLen(1))
			Expect(poll.OwnerReferences).To(HaveLen(1))
			Expect(http.Spec.Ports[0].Port).To(Equal(int32(8069)))
			Expect(poll.Spec.Ports[0].Port).To(Equal(int32(8072)))
			Expect(http.Spec.ClusterIP).NotTo(BeEmpty())

			again, err := EnsureHttpService(ctx, k8sClient, k8sClient.Scheme(), od)
			Expect(err).NotTo(HaveOccurred())
			Expect(again.Spec.ClusterIP).To(Equal(http.Spec.ClusterIP))
			Expect(again.ResourceVersion).To(Equal(http.ResourceVersion))
		})
	})

	Context("Secrets", func() {
		It("generates the admin password once and owns the secret", func() {
			s1, err := EnsureAdminPasswordSecret(ctx, k8sClient, k8sClient.Scheme(), od)
			Expect(err).NotTo(HaveOccurred())
			Expect(s1.Data["password"]).NotTo(BeEmpty())
			Expect(s1.OwnerReferences).To(HaveLen(1))

			s2, err := EnsureAdminPasswordSecret(ctx, k8sClient, k8sClient.Scheme(), od)
			Expect(err).NotTo(HaveOccurred())
			Expect(s2.Data["password"]).To(Equal(s1.Data["password"]), "the password must never rotate")
		})

		It("does not own a user-named admin secret", func() {
			od.Spec.Config.AdminPasswordSecretName = resourceName + "-custom-admin"
			s, err := EnsureAdminPasswordSecret(ctx, k8sClient, k8sClient.Scheme(), od)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Name).To(Equal(resourceName + "-custom-admin"))
			Expect(s.OwnerReferences).To(BeEmpty())
		})

		It("writes odoo.conf into the config secret and returns its hash", func() {
			s, hash, err := EnsureConfigSecret(ctx, k8sClient, k8sClient.Scheme(), od, "[options]\nworkers = 0\n")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(s.Data["odoo.conf"])).To(Equal("[options]\nworkers = 0\n"))
			Expect(hash).To(Equal(odoov1.ConfigHash("[options]\nworkers = 0\n")))
			Expect(s.OwnerReferences).To(HaveLen(1))

			s, hash2, err := EnsureConfigSecret(ctx, k8sClient, k8sClient.Scheme(), od, "[options]\nworkers = 2\n")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(s.Data["odoo.conf"])).To(Equal("[options]\nworkers = 2\n"))
			Expect(hash2).NotTo(Equal(hash))
		})
	})

	Context("Maintenance job", func() {
		It("creates the init job, adopts it on a second call, observes its states and deletes it", func() {
			job, err := EnsureMaintenanceJob(ctx, k8sClient, k8sClient.Scheme(), od, []string{"base", "web"}, nil, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(job.Name).To(Equal(resourceName + "-init"))
			Expect(job.OwnerReferences).To(HaveLen(1))
			Expect(job.Spec.Template.Spec.Containers[0].Command).To(ContainElements("-i", "base,web"))

			adopted, err := EnsureMaintenanceJob(ctx, k8sClient, k8sClient.Scheme(), od, []string{"base", "web"}, nil, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(adopted.UID).To(Equal(job.UID))

			od.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{Name: job.Name, Namespace: job.Namespace, Kind: "init"}
			obs, err := ObserveMaintenanceJob(ctx, k8sClient, k8sClient, od)
			Expect(err).NotTo(HaveOccurred())
			Expect(obs.State).To(Equal(JobStateActive))

			By("a failed job")
			failed, err := EnsureMaintenanceJob(ctx, k8sClient, k8sClient.Scheme(), od, nil, []string{"base"}, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(failed.Name).To(HavePrefix(resourceName + "-upgrade-"))
			DeferCleanup(func() { tryDelete(failed) })
			now := metav1.Now()
			failed.Status.StartTime = &now
			failed.Status.Failed = 3
			failed.Status.Conditions = []batchv1.JobCondition{
				{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit", LastTransitionTime: now},
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit", LastTransitionTime: now},
			}
			Expect(k8sClient.Status().Update(ctx, failed)).To(Succeed())
			od.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{Name: failed.Name, Namespace: failed.Namespace, Kind: "upgrade"}
			obs, err = ObserveMaintenanceJob(ctx, k8sClient, k8sClient, od)
			Expect(err).NotTo(HaveOccurred())
			Expect(obs.State).To(Equal(JobStateFailed))
			Expect(obs.FailureMessage).To(ContainSubstring("BackoffLimitExceeded"))

			By("a succeeded job")
			od.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{Name: job.Name, Namespace: job.Namespace, Kind: "init"}
			job.Status.StartTime = &now
			job.Status.CompletionTime = &now
			job.Status.Succeeded = 1
			job.Status.Conditions = []batchv1.JobCondition{
				{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now},
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now},
			}
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
			obs, err = ObserveMaintenanceJob(ctx, k8sClient, k8sClient, od)
			Expect(err).NotTo(HaveOccurred())
			Expect(obs.State).To(Equal(JobStateSucceeded))

			Expect(DeleteMaintenanceJob(ctx, k8sClient, job)).To(Succeed())
			Eventually(func() JobState {
				obs, _ := ObserveMaintenanceJob(ctx, k8sClient, k8sClient, od)
				return obs.State
			}).Should(Equal(JobStateMissing))
			Expect(DeleteMaintenanceJob(ctx, k8sClient, job)).To(Succeed(), "deleting twice is fine")
		})

		It("reports a quota rejection from the Job's events", func() {
			job, err := EnsureMaintenanceJob(ctx, k8sClient, k8sClient.Scheme(), od, []string{"base"}, nil, true)
			Expect(err).NotTo(HaveOccurred())
			event := &corev1.Event{
				ObjectMeta: metav1.ObjectMeta{Name: job.Name + ".quota", Namespace: resourceNamespace},
				InvolvedObject: corev1.ObjectReference{
					Kind: "Job", Namespace: resourceNamespace, Name: job.Name, UID: job.UID, APIVersion: "batch/v1",
				},
				Reason:  "FailedCreate",
				Message: `Error creating: pods "x" is forbidden: exceeded quota: ababiel-preview, requested: pods=1, used: pods=6, limited: pods=6`,
				Type:    corev1.EventTypeWarning,
				Source:  corev1.EventSource{Component: "job-controller"},
			}
			Expect(k8sClient.Create(ctx, event)).To(Succeed())
			DeferCleanup(func() { tryDelete(event) })

			od.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{Name: job.Name, Namespace: job.Namespace, Kind: "init"}
			obs, err := ObserveMaintenanceJob(ctx, k8sClient, k8sClient, od)
			Expect(err).NotTo(HaveOccurred())
			Expect(obs.State).To(Equal(JobStateActive))
			Expect(obs.QuotaMessage).To(ContainSubstring("exceeded quota"))
		})
	})

	Context("ReconcileDatabase", func() {
		conn := odoov1.DatabaseConnectionDetails{Host: "db-host", Port: 5432, User: "odoo", Password: "pw", Name: "odoo_db"}
		noPersist := func(context.Context) error { return nil }

		It("adopts a pre-existing database as external", func() {
			fake := database.NewFake()
			fake.Seed("odoo_db", "")
			res, err := ReconcileDatabase(ctx, fake, od, conn, noPersist)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Ready).To(BeTrue())
			Expect(res.Event).To(Equal(odoov1.ReasonDatabaseAdopted))
			Expect(od.Status.Database.ProvisionedBy).To(Equal("external"))
			Expect(od.Status.Database.State).To(Equal("Ready"))
			Expect(od.Status.Database.Name).To(Equal("odoo_db"))
			Expect(fake.CallCount("Create")).To(BeZero())
		})

		It("records Provisioning before creating a missing database", func() {
			fake := database.NewFake()
			persistedState := ""
			persist := func(context.Context) error {
				persistedState = od.Status.Database.State
				Expect(fake.CallCount("Create")).To(BeZero(), "status must be persisted before CREATE DATABASE")
				return nil
			}
			res, err := ReconcileDatabase(ctx, fake, od, conn, persist)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Ready).To(BeTrue())
			Expect(res.Event).To(Equal(odoov1.ReasonDatabaseCreated))
			Expect(persistedState).To(Equal("Provisioning"))
			Expect(od.Status.Database.ProvisionedBy).To(Equal("operator"))
			Expect(od.Status.Database.State).To(Equal("Ready"))
			Expect(od.Status.Database.CreatedAt).NotTo(BeNil())
			Expect(fake.Has("odoo_db")).To(BeTrue())
			Expect(fake.TagOf("odoo_db")).To(Equal(database.NewOwnerTag(resourceNamespace, resourceName)))
		})

		It("does not create with createPolicy Never", func() {
			fake := database.NewFake()
			od.Spec.Database.CreatePolicy = odoov1.DatabaseCreatePolicyNever
			res, err := ReconcileDatabase(ctx, fake, od, conn, noPersist)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Ready).To(BeFalse())
			Expect(res.Reason).To(Equal(odoov1.ReasonDatabaseMissing))
			Expect(od.Status.Database.Name).To(BeEmpty())
			Expect(fake.CallCount("Create")).To(BeZero())
		})

		It("recovers from a crash mid-create", func() {
			fake := database.NewFake()
			od.Status.Database = odoov1.OdooDatabaseStatus{Name: "odoo_db", Host: "db-host", ProvisionedBy: "operator", State: "Provisioning"}

			By("the database was never created")
			res, err := ReconcileDatabase(ctx, fake, od, conn, noPersist)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Ready).To(BeTrue())
			Expect(fake.CallCount("Create")).To(Equal(1))
			Expect(od.Status.Database.State).To(Equal("Ready"))

			By("the database was created but not tagged")
			fake2 := database.NewFake()
			fake2.Seed("odoo_db", "")
			od.Status.Database = odoov1.OdooDatabaseStatus{Name: "odoo_db", Host: "db-host", ProvisionedBy: "operator", State: "Provisioning"}
			res, err = ReconcileDatabase(ctx, fake2, od, conn, noPersist)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Ready).To(BeTrue())
			Expect(fake2.CallCount("Tag")).To(Equal(1))
			Expect(fake2.TagOf("odoo_db")).To(Equal(database.NewOwnerTag(resourceNamespace, resourceName)))
		})

		It("reports a fatal state when the resolved name changed", func() {
			fake := database.NewFake()
			od.Status.Database = odoov1.OdooDatabaseStatus{Name: "other_db", ProvisionedBy: "operator", State: "Ready"}
			res, err := ReconcileDatabase(ctx, fake, od, conn, noPersist)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Fatal).To(BeTrue())
			Expect(res.Reason).To(Equal(odoov1.ReasonDatabaseNameChanged))
			Expect(fake.Calls()).To(BeEmpty())
		})

		It("is stable once Ready", func() {
			fake := database.NewFake()
			od.Status.Database = odoov1.OdooDatabaseStatus{Name: "odoo_db", ProvisionedBy: "external", State: "Ready"}
			res, err := ReconcileDatabase(ctx, fake, od, conn, noPersist)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Ready).To(BeTrue())
			Expect(res.Event).To(BeEmpty())
			Expect(res.Reason).To(Equal(odoov1.ReasonDatabaseAdopted))
			Expect(fake.Calls()).To(BeEmpty())
		})

		It("surfaces connection errors", func() {
			fake := database.NewFake()
			fake.ExistsErr = fmt.Errorf("connection refused")
			res, err := ReconcileDatabase(ctx, fake, od, conn, noPersist)
			Expect(err).To(MatchError(ContainSubstring("connection refused")))
			Expect(res.Reason).To(Equal(odoov1.ReasonDatabaseConnectionFailed))
			Expect(od.Status.Database.Name).To(BeEmpty())
		})
	})
})

var _ = Describe("ConnInfoFor", func() {
	It("maps the connection details and the maintenance database", func() {
		od := newOdooDeployment("x", nil)
		od.Spec.Database.MaintenanceDatabase = "template1"
		info := ConnInfoFor(od, odoov1.DatabaseConnectionDetails{Host: "h", Port: 5433, User: "u", Password: "p", Name: "n", SSL: true})
		Expect(info).To(Equal(database.ConnInfo{Host: "h", Port: 5433, User: "u", Password: "p", MaintenanceDB: "template1", SSL: true}))
		Expect(types.NamespacedName{Name: od.Name}).NotTo(BeNil())
	})
})
