package controller

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/database"
)

var lifecycleCounter int64

const ns = "default"

// harness wraps one OdooDeployment and a reconciler backed by the fake provisioner.
type harness struct {
	name     string
	key      types.NamespacedName
	fake     *database.Fake
	recorder *record.FakeRecorder
	r        *OdooDeploymentReconciler
	dbSecret *corev1.Secret
}

func newHarness() *harness {
	n := atomic.AddInt64(&lifecycleCounter, 1)
	name := fmt.Sprintf("lc-%d", n)
	h := &harness{
		name:     name,
		key:      types.NamespacedName{Name: name, Namespace: ns},
		fake:     database.NewFake(),
		recorder: record.NewFakeRecorder(200),
	}
	// drain events so the recorder never blocks
	go func() {
		for range h.recorder.Events {
		}
	}()
	h.r = &OdooDeploymentReconciler{
		Client:    k8sClient,
		Scheme:    k8sClient.Scheme(),
		APIReader: k8sClient,
		DB:        h.fake,
		Recorder:  h.recorder,
	}
	h.dbSecret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-db", Namespace: ns},
		Data:       map[string][]byte{"password": []byte("secret\n"), "host": []byte("db-host\n")},
	}
	Expect(k8sClient.Create(ctx, h.dbSecret)).To(Succeed())
	return h
}

func (h *harness) spec(mutators ...func(*odoov1.OdooDeployment)) *odoov1.OdooDeployment {
	od := &odoov1.OdooDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: h.name, Namespace: ns},
		Spec: odoov1.OdooDeploymentSpec{
			Name:  h.name,
			Image: "odoo:a",
			Database: odoov1.OdooDatabaseConfig{
				HostFromSecret:     corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: h.name + "-db"}, Key: "host"},
				Port:               5432,
				User:               "odoo",
				Name:               "db_" + strings.ReplaceAll(h.name, "-", "_"),
				PasswordFromSecret: corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: h.name + "-db"}, Key: "password"},
				DeletionPolicy:     odoov1.DeletionPolicyDelete,
			},
			Modules: []string{"base", "web"},
			Upgrade: odoov1.OdooUpgradeConfig{Modules: []string{"base", "web"}},
			Config:  odoov1.OdooConfig{LoadLanguages: []string{"en_US", "ar_001"}},
			OdooFilestore: odoov1.PersistentVolumeClaimSpec{
				Size: resource.MustParse("1Gi"), DeletionPolicy: odoov1.DeletionPolicyDelete,
			},
		},
	}
	for _, m := range mutators {
		m(od)
	}
	return od
}

func (h *harness) dbName() string { return "db_" + strings.ReplaceAll(h.name, "-", "_") }

func (h *harness) create(mutators ...func(*odoov1.OdooDeployment)) *odoov1.OdooDeployment {
	od := h.spec(mutators...)
	Expect(k8sClient.Create(ctx, od)).To(Succeed())
	return od
}

// reconcile runs one reconcile and returns the result and the fresh object (nil when gone).
func (h *harness) reconcile() (ctrl.Result, *odoov1.OdooDeployment) {
	res, err := h.r.Reconcile(ctx, ctrl.Request{NamespacedName: h.key})
	Expect(err).NotTo(HaveOccurred())
	return res, h.get()
}

func (h *harness) get() *odoov1.OdooDeployment {
	od := &odoov1.OdooDeployment{}
	err := k8sClient.Get(ctx, h.key, od)
	if apierrors.IsNotFound(err) {
		return nil
	}
	Expect(err).NotTo(HaveOccurred())
	return od
}

func (h *harness) job(name string) *batchv1.Job {
	job := &batchv1.Job{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, job)
	if apierrors.IsNotFound(err) {
		return nil
	}
	Expect(err).NotTo(HaveOccurred())
	return job
}

func (h *harness) deployment() *appsv1.Deployment {
	d := &appsv1.Deployment{}
	err := k8sClient.Get(ctx, h.key, d)
	if apierrors.IsNotFound(err) {
		return nil
	}
	Expect(err).NotTo(HaveOccurred())
	return d
}

func (h *harness) succeedJob(name string) {
	job := h.job(name)
	Expect(job).NotTo(BeNil(), "job %s should exist", name)
	markJobSucceeded(job)
	Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
}

func (h *harness) failJob(name string) {
	job := h.job(name)
	Expect(job).NotTo(BeNil())
	markJobFailed(job)
	Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
}

// markJobSucceeded sets the terminal status the way the job controller does
// (the API server validates the transition: SuccessCriteriaMet before Complete).
func markJobSucceeded(job *batchv1.Job) {
	now := metav1.Now()
	job.Status.StartTime = &now
	job.Status.CompletionTime = &now
	job.Status.Succeeded = 1
	job.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now},
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now},
	}
}

func markJobFailed(job *batchv1.Job) {
	now := metav1.Now()
	job.Status.StartTime = &now
	job.Status.Failed = 3
	job.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit", LastTransitionTime: now},
		{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit", LastTransitionTime: now},
	}
}

// bootToRunning drives a fresh CR through init to Running and returns it.
func (h *harness) bootToRunning() *odoov1.OdooDeployment {
	_, od := h.reconcile()
	Expect(od.Status.Phase).To(Equal(odoov1.PhaseInitializing))
	h.succeedJob(h.name + "-init")
	_, od = h.reconcile()
	Expect(od.Status.Phase).To(Equal(odoov1.PhaseRunning))
	return od
}

func condition(od *odoov1.OdooDeployment, t string) *metav1.Condition {
	return meta.FindStatusCondition(od.Status.Conditions, t)
}

func (h *harness) cleanup() {
	for _, obj := range []client.Object{
		&odoov1.OdooDeployment{ObjectMeta: metav1.ObjectMeta{Name: h.name, Namespace: ns}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: h.name, Namespace: ns}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: h.name + "-http", Namespace: ns}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: h.name + "-poll", Namespace: ns}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: h.name + "-config", Namespace: ns}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: h.name + "-admin-password", Namespace: ns}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: h.name, Namespace: ns}},
		h.dbSecret,
	} {
		if err := k8sClient.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			Expect(err).NotTo(HaveOccurred())
		}
	}
	jobs := &batchv1.JobList{}
	Expect(k8sClient.List(ctx, jobs, client.InNamespace(ns), client.MatchingLabels{odoov1.LabelOdooDeployment: h.name})).To(Succeed())
	for i := range jobs.Items {
		_ = k8sClient.Delete(ctx, &jobs.Items[i])
	}
	// A CR stuck with our finalizer (a test that ended early) must not leak.
	if od := h.get(); od != nil && controllerutil.ContainsFinalizer(od, odoov1.DatabaseFinalizer) {
		controllerutil.RemoveFinalizer(od, odoov1.DatabaseFinalizer)
		_ = k8sClient.Update(ctx, od)
	}
}

var _ = Describe("OdooDeployment lifecycle", func() {
	var h *harness

	BeforeEach(func() { h = newHarness() })
	AfterEach(func() { h.cleanup() })

	It("boots a fresh CR: operator database, init job, then Deployment and services", func() {
		h.create()

		By("first reconcile: finalizer, database created, init job")
		res, od := h.reconcile()
		Expect(res.RequeueAfter).To(Equal(15 * time.Second))
		Expect(controllerutil.ContainsFinalizer(od, odoov1.DatabaseFinalizer)).To(BeTrue())
		Expect(od.Status.Database).To(MatchFields(IgnoreExtras, Fields{
			"Name":          Equal(h.dbName()),
			"Host":          Equal("db-host"),
			"ProvisionedBy": Equal("operator"),
			"State":         Equal("Ready"),
		}))
		Expect(h.fake.Has(h.dbName())).To(BeTrue())
		Expect(h.fake.TagOf(h.dbName())).To(Equal(database.NewOwnerTag(ns, h.name)))
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseInitializing))
		Expect(od.Status.CurrentInitJob.Name).To(Equal(h.name + "-init"))
		Expect(od.Status.CurrentInitJob.Kind).To(Equal("init"))
		Expect(od.Status.CurrentInitJob.Image).To(Equal("odoo:a"))
		Expect(od.Status.ObservedGeneration).To(Equal(od.Generation))
		Expect(condition(od, odoov1.ConditionDatabaseReady).Status).To(Equal(metav1.ConditionTrue))
		Expect(condition(od, odoov1.ConditionReady).Status).To(Equal(metav1.ConditionFalse))
		Expect(condition(od, odoov1.ConditionInitialized).Status).To(Equal(metav1.ConditionFalse))
		Expect(condition(od, odoov1.ConditionDegraded).Status).To(Equal(metav1.ConditionFalse))

		job := h.job(h.name + "-init")
		Expect(job).NotTo(BeNil())
		cmd := job.Spec.Template.Spec.Containers[0].Command
		Expect(strings.Join(cmd, " ")).To(Equal("odoo -c /opt/odoo/odoo.conf --stop-after-init --no-http -i base,web --load-language=en_US,ar_001"))
		Expect(job.Spec.Template.Spec.Containers[0].Image).To(Equal("odoo:a"))
		Expect(job.Spec.Template.Labels).NotTo(HaveKey("app"))
		Expect(h.deployment()).To(BeNil(), "no Deployment before the init job succeeds")

		By("the config secret locks the instance to its database")
		config := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: h.name + "-config", Namespace: ns}, config)).To(Succeed())
		Expect(string(config.Data["odoo.conf"])).To(ContainSubstring("list_db = False\ndbfilter = ^" + h.dbName() + "$\n"))
		Expect(string(config.Data["odoo.conf"])).To(ContainSubstring("db_host = db-host\n"), "secret values are trimmed")

		By("a second reconcile while the job runs is idle")
		res, od = h.reconcile()
		Expect(res.RequeueAfter).To(Equal(15 * time.Second))
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseInitializing))
		Expect(h.fake.CallCount("Create")).To(Equal(1))

		By("the init job succeeds")
		h.succeedJob(h.name + "-init")
		res, od = h.reconcile()
		Expect(od.Status.InitModulesInstalled).To(ConsistOf("base", "web"))
		Expect(od.Status.AppliedImage).To(Equal("odoo:a"))
		Expect(od.Status.CurrentInitJob.Name).To(BeEmpty())
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseRunning))
		Expect(condition(od, odoov1.ConditionInitialized).Status).To(Equal(metav1.ConditionTrue))
		Expect(condition(od, odoov1.ConditionReady).Reason).To(Equal(odoov1.ReasonDeploymentProgressing))
		Expect(res.RequeueAfter).To(Equal(30 * time.Second))
		Eventually(func() *batchv1.Job { return h.job(h.name + "-init") }).Should(BeNil(), "the finished job is deleted")

		d := h.deployment()
		Expect(d).NotTo(BeNil())
		Expect(*d.Spec.Replicas).To(Equal(int32(1)))
		Expect(d.Spec.Template.Spec.Containers[0].Image).To(Equal("odoo:a"))
		Expect(d.Spec.Template.Annotations).To(HaveKey(odoov1.AnnotationConfigHash))

		By("all six children are owned by the CR")
		for _, obj := range []client.Object{
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: h.name + "-admin-password", Namespace: ns}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: h.name + "-config", Namespace: ns}},
			&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: h.name, Namespace: ns}},
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: h.name, Namespace: ns}},
			&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: h.name + "-http", Namespace: ns}},
			&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: h.name + "-poll", Namespace: ns}},
		} {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
			owner := metav1.GetControllerOf(obj)
			Expect(owner).NotTo(BeNil(), "%T %s has no controller reference", obj, obj.GetName())
			Expect(owner.UID).To(Equal(od.UID))
		}

		By("the Deployment becoming available makes the CR Ready")
		d.Status.ObservedGeneration = d.Generation
		d.Status.Replicas, d.Status.AvailableReplicas, d.Status.ReadyReplicas = 1, 1, 1
		Expect(k8sClient.Status().Update(ctx, d)).To(Succeed())
		res, od = h.reconcile()
		Expect(res.RequeueAfter).To(BeZero())
		Expect(condition(od, odoov1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
		Expect(condition(od, odoov1.ConditionReady).Reason).To(Equal(odoov1.ReasonDeploymentAvailable))
		Expect(od.Status.ReadyReplicas).To(Equal(int32(1)))
	})

	It("upgrades in place on an image change: scale to zero, upgrade job, new image", func() {
		h.create()
		od := h.bootToRunning()

		By("changing the image")
		od.Spec.Image = "odoo:b"
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		res, od := h.reconcile()
		Expect(res.RequeueAfter).To(Equal(15 * time.Second))
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseUpgrading))
		Expect(od.Status.AppliedImage).To(Equal("odoo:a"), "the applied image only moves after the job succeeds")
		Expect(od.Status.CurrentInitJob.Name).To(HavePrefix(h.name + "-upgrade-"))
		Expect(od.Status.CurrentInitJob.Kind).To(Equal("upgrade"))
		Expect(od.Status.CurrentInitJob.UpgradeModules).To(ConsistOf("base", "web"))
		Expect(od.Status.CurrentInitJob.Modules).To(BeEmpty())

		d := h.deployment()
		Expect(*d.Spec.Replicas).To(BeZero())
		Expect(d.Spec.Template.Spec.Containers[0].Image).To(Equal("odoo:a"))

		job := h.job(od.Status.CurrentInitJob.Name)
		Expect(job).NotTo(BeNil())
		Expect(strings.Join(job.Spec.Template.Spec.Containers[0].Command, " ")).To(Equal("odoo -c /opt/odoo/odoo.conf --stop-after-init --no-http -u base,web"))
		Expect(job.Spec.Template.Spec.Containers[0].Image).To(Equal("odoo:b"))

		By("the upgrade job succeeds")
		h.succeedJob(od.Status.CurrentInitJob.Name)
		_, od = h.reconcile()
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseRunning))
		Expect(od.Status.AppliedImage).To(Equal("odoo:b"))
		Expect(od.Status.CurrentInitJob.Name).To(BeEmpty())
		d = h.deployment()
		Expect(*d.Spec.Replicas).To(Equal(int32(1)))
		Expect(d.Spec.Template.Spec.Containers[0].Image).To(Equal("odoo:b"))
	})

	It("waits for the old pods to stop before starting the upgrade job", func() {
		h.create()
		od := h.bootToRunning()

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: h.name + "-pod", Namespace: ns, Labels: map[string]string{"app": h.name}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "odoo", Image: "odoo:a"}}},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)) })

		od.Spec.Image = "odoo:b"
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		res, od := h.reconcile()
		Expect(res.RequeueAfter).To(Equal(10 * time.Second))
		Expect(condition(od, odoov1.ConditionReady).Reason).To(Equal(odoov1.ReasonWaitingForPodsToStop))
		Expect(od.Status.CurrentInitJob.Name).To(BeEmpty())
		Expect(*h.deployment().Spec.Replicas).To(BeZero())

		Expect(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0))).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{}))
		}).Should(BeTrue())
		_, od = h.reconcile()
		Expect(od.Status.CurrentInitJob.Name).To(HavePrefix(h.name + "-upgrade-"))
	})

	It("installs new modules and upgrades in one job, and rolls plainly without upgrade modules", func() {
		h.create()
		od := h.bootToRunning()

		By("new module + new image → one job with -i and -u")
		od.Spec.Modules = []string{"base", "web", "sale"}
		od.Spec.Image = "odoo:b"
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		job := h.job(od.Status.CurrentInitJob.Name)
		Expect(job).NotTo(BeNil())
		Expect(strings.Join(job.Spec.Template.Spec.Containers[0].Command, " ")).To(Equal(
			"odoo -c /opt/odoo/odoo.conf --stop-after-init --no-http -i sale -u base,web --load-language=en_US,ar_001"))
		h.succeedJob(job.Name)
		_, od = h.reconcile()
		Expect(od.Status.InitModulesInstalled).To(ConsistOf("base", "web", "sale"))
		Expect(od.Status.AppliedImage).To(Equal("odoo:b"))

		By("an image change with empty upgrade.modules rolls the Deployment without a job")
		od.Spec.Upgrade.Modules = nil
		od.Spec.Image = "odoo:c"
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		Expect(od.Status.CurrentInitJob.Name).To(BeEmpty())
		Expect(od.Status.AppliedImage).To(Equal("odoo:c"))
		Expect(h.deployment().Spec.Template.Spec.Containers[0].Image).To(Equal("odoo:c"))
		Expect(*h.deployment().Spec.Replicas).To(Equal(int32(1)))
	})

	It("honours onImageChange: false and the upgrade token", func() {
		f := false
		h.create(func(od *odoov1.OdooDeployment) { od.Spec.Upgrade.OnImageChange = &f })
		od := h.bootToRunning()

		od.Spec.Image = "odoo:b"
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		Expect(od.Status.CurrentInitJob.Name).To(BeEmpty())
		Expect(od.Status.AppliedImage).To(Equal("odoo:a"), "image changes are ignored")
		Expect(h.deployment().Spec.Template.Spec.Containers[0].Image).To(Equal("odoo:a"))

		od.Spec.Upgrade.Token = "release-2"
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		Expect(od.Status.CurrentInitJob.Kind).To(Equal("upgrade"))
		Expect(od.Status.CurrentInitJob.Token).To(Equal("release-2"))
		Expect(h.job(od.Status.CurrentInitJob.Name).Spec.Template.Spec.Containers[0].Image).To(Equal("odoo:b"))
		h.succeedJob(od.Status.CurrentInitJob.Name)
		_, od = h.reconcile()
		Expect(od.Status.AppliedImage).To(Equal("odoo:b"))
		Expect(od.Status.AppliedUpgradeToken).To(Equal("release-2"))
	})

	It("adopts a pre-existing database as external and never drops it", func() {
		h.fake.Seed(h.dbName(), "")
		h.create()
		_, od := h.reconcile()
		Expect(od.Status.Database.ProvisionedBy).To(Equal("external"))
		Expect(h.fake.CallCount("Create")).To(BeZero())
		Expect(controllerutil.ContainsFinalizer(od, odoov1.DatabaseFinalizer)).To(BeFalse(), "an external database releases the finalizer")

		Expect(k8sClient.Delete(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		Expect(od).To(BeNil())
		Expect(h.fake.CallCount("Drop")).To(BeZero())
		Expect(h.fake.Has(h.dbName())).To(BeTrue())
	})

	It("drops an operator-created database on delete and removes the finalizer", func() {
		h.create()
		od := h.bootToRunning()
		Expect(controllerutil.ContainsFinalizer(od, odoov1.DatabaseFinalizer)).To(BeTrue())

		Expect(k8sClient.Delete(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		Expect(od).To(BeNil(), "the finalizer is gone and the CR with it")
		Expect(h.fake.CallCount("Drop")).To(Equal(1))
		Expect(h.fake.Has(h.dbName())).To(BeFalse())
		Eventually(func() *appsv1.Deployment { return h.deployment() }).Should(BeNil())
	})

	It("refuses to drop a database whose comment does not match", func() {
		h.create()
		od := h.bootToRunning()
		h.fake.Seed(h.dbName(), "odoo-operator:other/owner") // someone re-tagged it

		Expect(k8sClient.Delete(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		Expect(od).To(BeNil())
		Expect(h.fake.CallCount("Drop")).To(Equal(1))
		Expect(h.fake.Has(h.dbName())).To(BeTrue(), "the mismatched database survives")
	})

	It("keeps the finalizer while the drop keeps failing", func() {
		h.create()
		od := h.bootToRunning()
		h.fake.DropErr = fmt.Errorf("connection refused")

		Expect(k8sClient.Delete(ctx, od)).To(Succeed())
		res, od := h.reconcile()
		Expect(od).NotTo(BeNil())
		Expect(res.RequeueAfter).To(Equal(30 * time.Second))
		Expect(condition(od, odoov1.ConditionDegraded).Reason).To(Equal(odoov1.ReasonDatabaseDropFailed))

		h.fake.DropErr = nil
		_, od = h.reconcile()
		Expect(od).To(BeNil())
		Expect(h.fake.Has(h.dbName())).To(BeFalse())
	})

	It("never adds a finalizer with deletionPolicy Retain", func() {
		h.create(func(od *odoov1.OdooDeployment) { od.Spec.Database.DeletionPolicy = odoov1.DeletionPolicyRetain })
		od := h.bootToRunning()
		Expect(od.Status.Database.ProvisionedBy).To(Equal("operator"))
		Expect(controllerutil.ContainsFinalizer(od, odoov1.DatabaseFinalizer)).To(BeFalse())

		Expect(k8sClient.Delete(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		Expect(od).To(BeNil())
		Expect(h.fake.CallCount("Drop")).To(BeZero())
		Expect(h.fake.Has(h.dbName())).To(BeTrue())
	})

	It("reports a missing database with createPolicy Never", func() {
		h.create(func(od *odoov1.OdooDeployment) { od.Spec.Database.CreatePolicy = odoov1.DatabaseCreatePolicyNever })
		res, od := h.reconcile()
		Expect(res.RequeueAfter).To(Equal(time.Minute))
		Expect(od.Status.Phase).To(Equal(odoov1.PhasePending))
		Expect(condition(od, odoov1.ConditionDegraded).Reason).To(Equal(odoov1.ReasonDatabaseMissing))
		Expect(h.fake.CallCount("Create")).To(BeZero())
		Expect(h.job(h.name + "-init")).To(BeNil())
	})

	It("goes Failed on a failed job and retries once the job is deleted", func() {
		h.create()
		h.reconcile()
		h.failJob(h.name + "-init")

		res, od := h.reconcile()
		Expect(res.RequeueAfter).To(Equal(2 * time.Minute))
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseFailed))
		Expect(condition(od, odoov1.ConditionDegraded).Reason).To(Equal(odoov1.ReasonInitJobFailed))
		Expect(condition(od, odoov1.ConditionDegraded).Message).To(ContainSubstring("kubectl -n default logs job/" + h.name + "-init"))
		Expect(h.job(h.name+"-init").DeletionTimestamp).To(BeNil(), "a failed job is kept for inspection")

		By("deleting the job means retry")
		Expect(k8sClient.Delete(ctx, h.job(h.name+"-init"), client.PropagationPolicy(metav1.DeletePropagationBackground))).To(Succeed())
		Eventually(func() *batchv1.Job { return h.job(h.name + "-init") }).Should(BeNil())
		res, od = h.reconcile()
		Expect(res.RequeueAfter).To(Equal(time.Second))
		Expect(od.Status.CurrentInitJob.Name).To(BeEmpty())
		_, od = h.reconcile()
		Expect(od.Status.CurrentInitJob.Name).To(Equal(h.name + "-init"))
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseInitializing))
		Expect(h.job(h.name + "-init")).NotTo(BeNil())
	})

	It("is Ready at replicas 0", func() {
		zero := int32(0)
		h.create(func(od *odoov1.OdooDeployment) { od.Spec.Replicas = &zero })
		od := h.bootToRunning()
		Expect(*od.Spec.Replicas).To(BeZero(), "replicas: 0 must survive the round trip")
		Expect(*h.deployment().Spec.Replicas).To(BeZero())
		Expect(condition(od, odoov1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
		Expect(condition(od, odoov1.ConditionReady).Reason).To(Equal(odoov1.ReasonScaledToZero))
	})

	It("adopts a CR reconciled by 0.2.x without running a job", func() {
		h.fake.Seed(h.dbName(), "")
		od := h.create()
		od.Status.InitModulesInstalled = []string{"base", "web"}
		Expect(k8sClient.Status().Update(ctx, od)).To(Succeed())

		_, od = h.reconcile()
		Expect(od.Status.AppliedImage).To(Equal("odoo:a"))
		Expect(od.Status.CurrentInitJob.Name).To(BeEmpty())
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseRunning))
		Expect(h.deployment()).NotTo(BeNil())
	})

	It("stays Pending when the database connection cannot be resolved", func() {
		h.create(func(od *odoov1.OdooDeployment) {
			od.Spec.Database.PasswordFromSecret.Name = "does-not-exist"
		})
		res, od := h.reconcile()
		Expect(res.RequeueAfter).To(Equal(30 * time.Second))
		Expect(od.Status.Phase).To(Equal(odoov1.PhasePending))
		Expect(condition(od, odoov1.ConditionDegraded).Reason).To(Equal(odoov1.ReasonDatabaseConnectionFailed))
	})

	It("reports invalid extraOptions keys as Degraded/InvalidSpec", func() {
		h.create(func(od *odoov1.OdooDeployment) {
			od.Spec.Config.ExtraOptions = map[string]odoov1.OdooOptionValue{"Bad Key": "x", "smtp_server": "mail"}
		})
		res, od := h.reconcile()
		Expect(res.RequeueAfter).To(BeZero())
		Expect(od.Status.Phase).To(Equal(odoov1.PhasePending))
		Expect(condition(od, odoov1.ConditionDegraded).Reason).To(Equal(odoov1.ReasonInvalidSpec))
		Expect(condition(od, odoov1.ConditionDegraded).Message).To(ContainSubstring("Bad Key"))
		Expect(h.fake.Calls()).To(BeEmpty())
	})

	It("rolls the pods when the configuration changes", func() {
		h.create()
		od := h.bootToRunning()
		before := h.deployment().Spec.Template.Annotations[odoov1.AnnotationConfigHash]

		workers := int32(0)
		od.Spec.Config.Workers = &workers
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		Expect(*od.Spec.Config.Workers).To(BeZero(), "workers: 0 must survive the round trip")
		after := h.deployment().Spec.Template.Annotations[odoov1.AnnotationConfigHash]
		Expect(after).NotTo(Equal(before))
		config := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: h.name + "-config", Namespace: ns}, config)).To(Succeed())
		Expect(string(config.Data["odoo.conf"])).To(ContainSubstring("workers = 0\n"))
	})
})

var _ = Describe("CRD admission", func() {
	var h *harness
	BeforeEach(func() { h = newHarness() })
	AfterEach(func() { h.cleanup() })

	It("rejects renaming the database", func() {
		od := h.create()
		od.Spec.Database.Name = "renamed"
		err := k8sClient.Update(ctx, od)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("spec.database.name is immutable"))

		od = h.get()
		od.Spec.Database.NameFromSecret = corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "s"}, Key: "k"}
		err = k8sClient.Update(ctx, od)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.database.nameFromSecret is immutable"))
	})

	It("round-trips zero values and applies defaults", func() {
		zero := int32(0)
		f := false
		h.create(func(od *odoov1.OdooDeployment) {
			od.Spec.Replicas = &zero
			od.Spec.Config.Workers = &zero
			od.Spec.Config.WithoutDemo = &f
		})
		od := h.get()
		Expect(od.Spec.Replicas).To(HaveValue(Equal(int32(0))))
		Expect(od.Spec.Config.Workers).To(HaveValue(Equal(int32(0))))
		Expect(od.Spec.Config.WithoutDemo).To(HaveValue(BeFalse()))
		Expect(od.Spec.Config.ProxyMode).To(HaveValue(BeTrue()), "defaulted")
		Expect(od.Spec.Config.MaxCronThreads).To(HaveValue(Equal(int32(1))), "defaulted")
		Expect(od.Spec.Config.ListDb).To(HaveValue(BeFalse()), "defaulted")
		Expect(od.Spec.Upgrade.OnImageChange).To(HaveValue(BeTrue()), "defaulted through the empty upgrade object")
		Expect(od.Spec.Jobs.ActiveDeadlineSeconds).To(HaveValue(Equal(int64(3600))))
		Expect(od.Spec.Jobs.TTLSecondsAfterFinished).To(HaveValue(Equal(int32(86400))))
		Expect(od.Spec.Jobs.BackoffLimit).To(HaveValue(Equal(int32(2))))
		Expect(od.Spec.Probes.Enabled).To(HaveValue(BeTrue()))
		Expect(od.Spec.Database.CreatePolicy).To(Equal(odoov1.DatabaseCreatePolicyIfNotExists))
		Expect(od.Spec.Database.MaintenanceDatabase).To(Equal("postgres"))
		Expect(od.Spec.OdooFilestore.DeletionPolicy).To(Equal(odoov1.DeletionPolicyDelete))
	})

	DescribeTable("rejects invalid specs",
		func(mutate func(*odoov1.OdooDeployment), wantErr string) {
			od := h.spec(mutate)
			err := k8sClient.Create(ctx, od)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), err.Error())
			Expect(err.Error()).To(ContainSubstring(wantErr))
		},
		Entry("bad module name", func(od *odoov1.OdooDeployment) { od.Spec.Modules = []string{"base", "my-module"} }, "spec.modules"),
		Entry("bad upgrade module name", func(od *odoov1.OdooDeployment) { od.Spec.Upgrade.Modules = []string{"a b"} }, "spec.upgrade.modules"),
		Entry("negative replicas", func(od *odoov1.OdooDeployment) { r := int32(-1); od.Spec.Replicas = &r }, "spec.replicas"),
		Entry("bad database name", func(od *odoov1.OdooDeployment) { od.Spec.Database.Name = "1bad;drop" }, "spec.database.name"),
		Entry("multi-line dbFilter", func(od *odoov1.OdooDeployment) { od.Spec.Config.DbFilter = "^a$\nlist_db = True" }, "spec.config.dbFilter"),
		Entry("multi-line extraOptions value", func(od *odoov1.OdooDeployment) {
			od.Spec.Config.ExtraOptions = map[string]odoov1.OdooOptionValue{"smtp_server": "a\nb"}
		}, "extraOptions"),
		Entry("bad language code", func(od *odoov1.OdooDeployment) { od.Spec.Config.LoadLanguages = []string{"english"} }, "spec.config.loadLanguages"),
		Entry("bad createPolicy", func(od *odoov1.OdooDeployment) { od.Spec.Database.CreatePolicy = "Always" }, "spec.database.createPolicy"),
	)
})
