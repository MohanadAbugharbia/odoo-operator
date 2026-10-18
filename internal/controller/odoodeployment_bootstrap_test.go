package controller

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// newBootstrapHarness is a new instance that starts from a backup file in a
// readOnly store without a key.
func newBootstrapHarness(neutralize bool) *backupHarness {
	h := &backupHarness{harness: newHarness()}
	h.r.BackupImage = "ghcr.io/example/odoo-operator-backup:test"
	h.r.Version = "test"
	h.store = h.name + "-old"
	h.track(&odoov1.OdooBackupStore{
		ObjectMeta: metav1.ObjectMeta{Name: h.store, Namespace: ns},
		Spec:       odoov1.OdooBackupStoreSpec{PVC: &odoov1.PVCStore{ClaimName: "exports"}, ReadOnly: true},
	})
	h.create(func(od *odoov1.OdooDeployment) {
		od.Spec.Bootstrap = &odoov1.Bootstrap{FromBackup: &odoov1.BootstrapFromBackup{
			BackupSource: odoov1.BackupSource{Store: h.store, Path: "exports/shop_*.zip"},
			Neutralize:   neutralize,
		}}
	})
	return h
}

func (h *backupHarness) bootstrapRestore() *odoov1.OdooRestore {
	return h.restore("bootstrap")
}

var _ = Describe("Bootstrap from a backup", func() {
	var h *backupHarness

	AfterEach(func() {
		if h != nil {
			if rs := h.bootstrapRestore(); rs != nil {
				h.objs = append(h.objs, rs)
			}
			h.cleanupAll()
		}
	})

	It("restores the backup instead of initializing a new database", func() {
		h = newBootstrapHarness(true)
		_, od := h.reconcile()
		rs := h.bootstrapRestore()
		Expect(rs).NotTo(BeNil())
		Expect(rs.Spec.OdooDeployment).To(Equal(h.name))
		Expect(rs.Spec.From).To(Equal(&odoov1.BackupSource{Store: h.store, Path: "exports/shop_*.zip"}))
		Expect(rs.Spec.Neutralize).To(BeTrue())
		Expect(rs.Labels[odoov1.LabelOdooDeployment]).To(Equal(h.name))
		Expect(od.Status.Bootstrap.Restore).To(Equal(rs.Name))
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseInitializing))
		Expect(h.job(h.name + "-init")).To(BeNil())

		_, od = h.reconcile()
		op := od.Status.CurrentOperation
		Expect(op.Kind).To(Equal(odoov1.OperationRestore))
		Expect(op.Name).To(Equal(rs.Name))
		job := h.job(op.Job)
		Expect(job).NotTo(BeNil())
		args := jobArgs(job)
		Expect(args).To(ContainSubstring("--file exports/shop_*.zip"))
		Expect(args).To(ContainSubstring("--neutralize"))
		Expect(args).To(ContainSubstring("--no-lock"))
		Expect(args).NotTo(ContainSubstring("--snapshot"))
		init := job.Spec.Template.Spec.InitContainers
		Expect(init).To(HaveLen(2))
		Expect(init[1].Image).To(Equal("odoo:a"), "the instance's own image reports the version and neutralizes")
		for _, e := range job.Spec.Template.Spec.Containers[0].Env {
			Expect(e.Name).NotTo(ContainSubstring("RESTIC_PASSWORD"), "the store has no key")
		}
		Expect(h.bootstrapRestore().Status.File).To(Equal("exports/shop_*.zip"))

		h.finishJob(op.Job, &result.Result{
			OK: true, LiveChanged: true, PreviousKept: true, OdooVersion: "18.0",
			File: "exports/shop_2026-02-01.zip",
		})
		_, od = h.reconcile()
		rs = h.bootstrapRestore()
		Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseCompleted))
		Expect(rs.Status.File).To(Equal("exports/shop_2026-02-01.zip"))
		Expect(od.Status.Bootstrap).To(Equal(odoov1.BootstrapStatus{Restore: rs.Name, Completed: true}))
		Expect(od.Status.InitModulesInstalled).To(ConsistOf("base", "web"))
		Expect(od.Status.AppliedImage).To(Equal("odoo:a"))

		for i := 0; i < 3; i++ {
			_, od = h.reconcile()
		}
		Expect(h.job(h.name+"-init")).To(BeNil(), "Odoo never initializes over the restored data")
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseRunning))
		Expect(h.replicas()).To(Equal(int32(1)))

		By("a deleted bootstrap restore is not made again")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rs), rs)).To(Succeed())
		if controllerutil.RemoveFinalizer(rs, odoov1.RestoreFinalizer) {
			Expect(k8sClient.Update(ctx, rs)).To(Succeed())
		}
		Expect(k8sClient.Delete(ctx, rs)).To(Succeed())
		Eventually(func() *odoov1.OdooRestore { return h.bootstrapRestore() }).Should(BeNil())
		h.reconcile()
		Expect(h.bootstrapRestore()).To(BeNil())
	})

	It("counts a restored database as initialized even when spec.modules is empty", func() {
		h = newBootstrapHarness(false)
		od := h.get()
		od.Spec.Modules = nil
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		h.reconcile()
		_, od = h.reconcile()
		job := h.job(od.Status.CurrentOperation.Job)
		Expect(jobArgs(job)).NotTo(ContainSubstring("--neutralize"))
		Expect(job.Spec.Template.Spec.InitContainers).To(HaveLen(2), "the Odoo step still reports the version")
		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: true, LiveChanged: true, File: "exports/shop.zip"})
		_, od = h.reconcile()
		Expect(od.Status.InitModulesInstalled).To(Equal([]string{"base"}))
		h.reconcile()
		Expect(h.job(h.name + "-init")).To(BeNil())
	})

	It("waits on a failed restore and tries again once it is deleted", func() {
		h = newBootstrapHarness(false)
		h.reconcile()
		_, od := h.reconcile()
		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{
			OK: false, Step: result.StepFetch, Error: `no file in "exports" matches shop_*.zip`,
		})
		for i := 0; i < 3; i++ {
			_, od = h.reconcile()
		}
		rs := h.bootstrapRestore()
		Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseFailed))
		Expect(h.job(h.name+"-init")).To(BeNil(), "an empty database is never initialized instead")
		Expect(h.replicas()).To(Equal(int32(0)), "Odoo does not start")
		cond := condition(od, odoov1.ConditionDegraded)
		Expect(cond.Reason).To(Equal(odoov1.ReasonBootstrapFailed))
		Expect(cond.Message).To(ContainSubstring("matches shop_*.zip"))
		Expect(cond.Message).To(ContainSubstring("delete it to try again"))
		Expect(od.Status.Bootstrap.Completed).To(BeFalse())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rs), rs)).To(Succeed())
		if controllerutil.RemoveFinalizer(rs, odoov1.RestoreFinalizer) {
			Expect(k8sClient.Update(ctx, rs)).To(Succeed())
		}
		Expect(k8sClient.Delete(ctx, rs)).To(Succeed())
		Eventually(func() *odoov1.OdooRestore { return h.bootstrapRestore() }).Should(BeNil())
		h.reconcile()
		again := h.bootstrapRestore()
		Expect(again).NotTo(BeNil())
		Expect(again.UID).NotTo(Equal(rs.UID))
		_, od = h.reconcile()
		Expect(od.Status.CurrentOperation.Name).To(Equal(again.Name), "%+v %+v", od.Status.CurrentOperation, h.bootstrapRestore().Status)
	})

	It("refuses a store without a key for a restic repository", func() {
		h = newBootstrapHarness(false)
		od := h.get()
		od.Spec.Bootstrap.FromBackup.Path = "other-ns/odoo"
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		for i := 0; i < 3; i++ {
			h.reconcile()
		}
		rs := h.bootstrapRestore()
		Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseFailed))
		Expect(rs.Status.Message).To(ContainSubstring("has no encryptionKeySecret"))
	})
})

var _ = Describe("ReadOnly stores", func() {
	var h *backupHarness

	AfterEach(func() {
		if h != nil {
			h.cleanupAll()
		}
	})

	readOnly := func(h *backupHarness, name string) {
		h.track(&odoov1.OdooBackupStore{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       odoov1.OdooBackupStoreSpec{PVC: &odoov1.PVCStore{ClaimName: "exports"}, ReadOnly: true},
		})
	}

	It("are never written: not as the backup store, not for copies", func() {
		h = newBackupHarness()
		ro := h.name + "-ro"
		readOnly(h, ro)

		od := h.get()
		od.Spec.Backup.Stores = []string{ro}
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		h.newBackup("one")
		_, od = h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
		b := h.backup("one")
		Expect(b.Status.Phase).To(Equal(odoov1.BackupPhaseFailed))
		Expect(b.Status.Message).To(ContainSubstring("is readOnly"))

		od.Spec.Backup.Stores = []string{h.store, ro}
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		h.newBackup("two")
		_, od = h.reconcile()
		job := h.job(od.Status.CurrentOperation.Job)
		Expect(job).NotTo(BeNil())
		Expect(jobArgs(job)).NotTo(ContainSubstring(ro))
		b = h.backup("two")
		Expect(b.Status.Copies).To(HaveLen(1))
		Expect(b.Status.Copies[0].Store).To(Equal(ro))
		Expect(b.Status.Copies[0].Error).To(ContainSubstring("is readOnly"))
	})

	It("restore from another instance's repository without locks", func() {
		h = newBackupHarness()
		ro := h.name + "-ro"
		key := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: ro + "-key", Namespace: ns},
			Data:       map[string][]byte{"password": []byte("other")},
		}
		h.track(key, &odoov1.OdooBackupStore{
			ObjectMeta: metav1.ObjectMeta{Name: ro, Namespace: ns},
			Spec: odoov1.OdooBackupStoreSpec{
				PVC: &odoov1.PVCStore{ClaimName: "old-backups"}, ReadOnly: true,
				EncryptionKeySecret: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: key.Name}, Key: "password"},
			},
		})
		h.newRestore("r", func(s *odoov1.OdooRestoreSpec) {
			s.From = &odoov1.BackupSource{Store: ro, Path: "prod/my-odoo", Host: "prod/my-odoo", Tags: []string{"kind=manual"}}
		})
		_, od := h.reconcile()
		job := h.job(od.Status.CurrentOperation.Job)
		Expect(job).NotTo(BeNil())
		args := jobArgs(job)
		for _, want := range []string{"--snapshot latest", "--host prod/my-odoo", "--tag kind=manual", "--no-lock"} {
			Expect(args).To(ContainSubstring(want))
		}
		var repo string
		for _, e := range job.Spec.Template.Spec.Containers[0].Env {
			if strings.HasSuffix(e.Name, "RESTIC_REPOSITORY") {
				repo = e.Value
			}
		}
		Expect(repo).To(HaveSuffix("/prod/my-odoo"))
		Expect(h.restore("r").Status.Store).To(Equal(ro))

		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{
			OK: true, LiveChanged: true, PreviousKept: true, Image: "odoo:prod", UpgradeToken: "p1",
			SnapshotID: strings.Repeat("ab", 32),
		})
		_, od = h.reconcile()
		rs := h.restore("r")
		Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseCompleted))
		Expect(rs.Status.SnapshotID).To(Equal(strings.Repeat("ab", 32)), "the snapshot latest resolved to")
		Expect(od.Status.AppliedImage).To(Equal("odoo:prod"), "a backup made by the operator records its image")
		Expect(od.Status.AppliedUpgradeToken).To(Equal("p1"))
	})
})
