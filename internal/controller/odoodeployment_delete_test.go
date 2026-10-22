package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
	"github.com/MohanadAbugharbia/odoo-operator/internal/database"
)

// ownedPod makes pod count as one of the instance's, which its deletion
// waits for.
func (h *backupHarness) ownedPod(pod *corev1.Pod) {
	pod.Labels[odoov1.LabelOdooDeployment] = h.name
	ExpectWithOffset(1, k8sClient.Update(ctx, pod)).To(Succeed())
}

// endPod lets pod end with res as its main container's result.
func endPod(pod *corev1.Pod, res result.Result) {
	ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(Succeed())
	pod.Status.Phase = corev1.PodSucceeded
	if !res.OK {
		pod.Status.Phase = corev1.PodFailed
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{*mainResult(res)}
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, pod)).To(Succeed())
}

// reconcileWork runs the OdooBackup and OdooRestore reconcilers on name.
func reconcileWork(name string) {
	req := ctrl.Request{NamespacedName: client.ObjectKey{Namespace: ns, Name: name}}
	_, err := (&OdooBackupReconciler{Client: k8sClient}).Reconcile(ctx, req)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	_, err = (&OdooRestoreReconciler{Client: k8sClient}).Reconcile(ctx, req)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

var _ = Describe("Deleting an instance with its database", func() {
	var h *backupHarness

	AfterEach(func() {
		if h != nil {
			h.cleanupAll()
		}
	})

	It("drops the copies of a restore it cut short, which its Job swapped in unrecorded", func() {
		h = newBackupHarness()
		h.completedBackup()
		h.newRestore("r", func(s *odoov1.OdooRestoreSpec) { s.Backup = h.name + "-one" })
		_, od := h.reconcile()
		job := od.Status.CurrentOperation.Job
		Expect(job).NotTo(BeEmpty())
		suffix := h.restore("r").Status.Suffix
		pod := h.attemptPod(job, "a", podStatus{
			phase: corev1.PodRunning, steps: []corev1.ContainerStatus{stepDone("load"), stepDone("odoo")}, main: mainRunning(),
		})
		h.ownedPod(pod)

		Expect(k8sClient.Delete(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		Expect(od).NotTo(BeNil(), "the deletion waits for the restore's pod")

		By("the OdooRestore reconciler leaves the restore of an instance being deleted alone")
		reconcileWork(h.name + "-r")
		rs := h.restore("r")
		Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseRestoring))
		Expect(rs.Status.Job).To(Equal(job))
		Expect(rs.Finalizers).To(ContainElement(odoov1.RestoreFinalizer))

		By("the Job swaps the data in: the replaced data is kept, owner-tagged; a half-loaded copy carries no comment")
		tag := database.NewOwnerTag(ns, h.name)
		prev, loaded := layout.PrevName(h.dbName(), suffix), layout.RestoreName(h.dbName(), suffix)
		h.fake.Seed(prev, tag)
		h.fake.Seed(loaded, "")
		endPod(pod, result.Result{OK: true, LiveChanged: true, PreviousKept: true})
		for i := 0; i < 3 && h.get() != nil; i++ {
			h.reconcile()
		}
		Expect(h.get()).To(BeNil())
		Expect(h.fake.Has(h.dbName())).To(BeFalse())
		Expect(h.fake.Has(prev)).To(BeFalse(), "the replaced data goes with the instance")
		Expect(h.fake.Has(loaded)).To(BeFalse(), "the copy is dropped by its name")
		Expect(h.fake.CallCount("DropCopy")).To(Equal(1))

		rs = h.restore("r")
		Expect(rs).NotTo(BeNil())
		Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseFailed))
		Expect(rs.Status.Message).To(ContainSubstring("was deleted while the restore was running"))
		Expect(rs.Status.Message).To(ContainSubstring("dropped the databases the restore made"))
		Expect(rs.Status.CleanedUp).To(BeTrue())
		Expect(rs.Status.Job).To(BeEmpty())
	})

	It("drops the leftovers of a failed restore not yet cleaned up, never another owner's", func() {
		h = newBackupHarness()
		suffix := "20250102T030405"
		rs := h.newRestore("failed", func(s *odoov1.OdooRestoreSpec) { s.SnapshotID = completedSnapshot })
		rs.Status.Phase = odoov1.RestorePhaseFailed
		rs.Status.Suffix = suffix
		Expect(k8sClient.Status().Update(ctx, rs)).To(Succeed())
		other := "20250102T030406"
		foreign := h.newRestore("foreign", func(s *odoov1.OdooRestoreSpec) { s.SnapshotID = completedSnapshot })
		foreign.Status.Phase = odoov1.RestorePhaseFailed
		foreign.Status.Suffix = other
		Expect(k8sClient.Status().Update(ctx, foreign)).To(Succeed())
		loaded := layout.RestoreName(h.dbName(), suffix)
		h.fake.Seed(loaded, "")
		h.fake.Seed(layout.RestoreName(h.dbName(), other), "odoo-operator:elsewhere/odoo")

		Expect(k8sClient.Delete(ctx, h.get())).To(Succeed())
		for i := 0; i < 3 && h.get() != nil; i++ {
			h.reconcile()
		}
		Expect(h.get()).To(BeNil())
		Expect(h.fake.Has(loaded)).To(BeFalse())
		Expect(h.restore("failed").Status.CleanedUp).To(BeTrue())
		Expect(h.fake.Has(layout.RestoreName(h.dbName(), other))).To(BeTrue(), "a copy another owner tagged stays")
		Expect(h.restore("foreign").Status.CleanedUp).To(BeFalse())
	})

	It("records the snapshot of a backup whose Job finished during the deletion", func() {
		h = newBackupHarness()
		h.newBackup("b")
		_, od := h.reconcile()
		job := od.Status.CurrentOperation.Job
		Expect(job).NotTo(BeEmpty())
		pod := h.attemptPod(job, "a", podStatus{phase: corev1.PodRunning, main: mainRunning()})
		h.ownedPod(pod)

		Expect(k8sClient.Delete(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		Expect(od).NotTo(BeNil())
		reconcileWork(h.name + "-b")
		Expect(h.backup("b").Status.Phase).To(Equal(odoov1.BackupPhaseRunning), "never failed while its Job may still write")

		endPod(pod, result.Result{OK: true, SnapshotID: "fedcba9876543210", DatabaseBytes: 1, FilestoreFiles: 1})
		h.endJob(job, 0, true)
		for i := 0; i < 3 && h.get() != nil; i++ {
			h.reconcile()
		}
		Expect(h.get()).To(BeNil())
		reconcileWork(h.name + "-b")
		b := h.backup("b")
		Expect(b.Status.Phase).To(Equal(odoov1.BackupPhaseCompleted), b.Status.Message)
		Expect(b.Status.SnapshotID).To(Equal("fedcba9876543210"))
	})

	It("reports the reason of a backup whose Job failed during the deletion", func() {
		h = newBackupHarness(func(od *odoov1.OdooDeployment) {
			od.Spec.Database.DeletionPolicy = odoov1.DeletionPolicyRetain
		})
		h.newBackup("b")
		_, od := h.reconcile()
		job := od.Status.CurrentOperation.Job
		pod := h.attemptPod(job, "a", podStatus{phase: corev1.PodRunning, main: mainRunning()})
		h.ownedPod(pod)
		Expect(k8sClient.Delete(ctx, od)).To(Succeed())
		h.reconcile()
		endPod(pod, result.Result{OK: false, Step: result.StepUpload, Error: "store full"})
		h.endJob(job, 1, false)
		for i := 0; i < 3 && h.get() != nil; i++ {
			h.reconcile()
		}
		Expect(h.get()).To(BeNil())
		b := h.backup("b")
		Expect(b.Status.Phase).To(Equal(odoov1.BackupPhaseFailed))
		Expect(b.Status.Message).To(ContainSubstring("store full"))
	})
})
