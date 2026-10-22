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
	"github.com/MohanadAbugharbia/odoo-operator/internal/database"
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

	It("upgrades the restored database to spec.image when asked, and then needs no upgrade Job", func() {
		h = newBootstrapHarness(false)
		od := h.get()
		od.Spec.Bootstrap.FromBackup.Upgrade = true
		od.Spec.Upgrade.Modules = []string{"shop", "pos"}
		od.Spec.Upgrade.Token = "t1"
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		h.reconcile()
		Expect(h.bootstrapRestore().Spec.Upgrade).To(BeTrue())

		_, od = h.reconcile()
		job := h.job(od.Status.CurrentOperation.Job)
		Expect(job).NotTo(BeNil())
		args := jobArgs(job)
		Expect(args).To(ContainSubstring("--upgrade shop,pos"))
		Expect(args).To(ContainSubstring("--upgrade-image odoo:a"))
		odooStep := job.Spec.Template.Spec.InitContainers[1]
		Expect(odooStep.Image).To(Equal("odoo:a"))
		Expect(odooStep.Env).To(ContainElement(corev1.EnvVar{Name: "UPGRADE", Value: "shop,pos"}))

		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{
			OK: true, LiveChanged: true, PreviousKept: true, OdooVersion: "18.0", Image: "odoo:a",
			File: "exports/shop_2026-02-01.zip",
		})
		_, od = h.reconcile()
		Expect(h.bootstrapRestore().Status.Phase).To(Equal(odoov1.RestorePhaseCompleted))
		Expect(od.Status.AppliedImage).To(Equal("odoo:a"))
		Expect(od.Status.AppliedUpgradeToken).To(Equal("t1"))
		for i := 0; i < 3; i++ {
			_, od = h.reconcile()
		}
		Expect(h.job(od.MaintenanceJobName(nil, false))).To(BeNil(), "the restore already upgraded the data")
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseRunning), "status: %+v", od.Status)
	})

	It("upgrades all modules when spec.upgrade.modules is empty", func() {
		h = newBootstrapHarness(true)
		od := h.get()
		od.Spec.Bootstrap.FromBackup.Upgrade = true
		od.Spec.Upgrade.Modules = nil
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		h.reconcile()
		_, od = h.reconcile()
		args := jobArgs(h.job(od.Status.CurrentOperation.Job))
		Expect(args).To(ContainSubstring("--upgrade all"))
		Expect(args).To(ContainSubstring("--neutralize"))
	})

	It("goes with its instance: the kept database is dropped and the bootstrap OdooRestore deleted", func() {
		h = newBootstrapHarness(false)
		h.reconcile()
		rs := h.bootstrapRestore()
		od := h.get()
		Expect(rs.OwnerReferences).To(ContainElement(HaveField("UID", od.UID)), "owned, so it goes with the instance")
		Expect(rs.OwnerReferences[0].Controller).To(BeNil(), "owned, not controlled")

		_, od = h.reconcile()
		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: true, LiveChanged: true, PreviousKept: true, File: "exports/shop.zip"})
		h.reconcile()
		rs = h.bootstrapRestore()
		prev := rs.Status.Previous.Database
		Expect(rs.Status.Previous.State).To(Equal(odoov1.PreviousKept))
		Expect(prev).NotTo(BeEmpty())
		tag := database.NewOwnerTag(ns, h.name)
		h.fake.Seed(prev, tag)
		other := h.name + "-other"
		h.fake.Seed(other, "someone-else")
		user := &odoov1.OdooRestore{
			ObjectMeta: metav1.ObjectMeta{Name: h.name + "-user", Namespace: ns},
			Spec:       odoov1.OdooRestoreSpec{OdooDeployment: h.name, From: &odoov1.BackupSource{Store: h.store, Path: "exports/shop.zip"}},
		}
		h.track(user)
		user.Status.Phase = odoov1.RestorePhaseCompleted
		user.Status.Previous = odoov1.RestorePrevious{State: odoov1.PreviousKept, Database: other}
		Expect(k8sClient.Status().Update(ctx, user)).To(Succeed())

		Expect(k8sClient.Delete(ctx, h.get())).To(Succeed())
		for i := 0; i < 5 && h.get() != nil; i++ {
			h.reconcile()
		}
		Expect(h.get()).To(BeNil())
		Expect(h.fake.Has(h.dbName())).To(BeFalse())
		Expect(h.fake.Has(prev)).To(BeFalse(), "the database the bootstrap kept goes with the instance")
		Expect(h.fake.Has(other)).To(BeTrue(), "a database another owner tagged is never dropped")
		if rs := h.bootstrapRestore(); rs != nil {
			Expect(rs.DeletionTimestamp).NotTo(BeNil(), "the bootstrap OdooRestore is deleted with the instance")
			Expect(rs.Status.Previous.State).To(Equal(odoov1.PreviousRemoved))
		}
		Expect(h.restore("user")).NotTo(BeNil(), "an OdooRestore someone created stays")
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
		d := h.deployment()
		Expect(d == nil || *d.Spec.Replicas == 0).To(BeTrue(), "Odoo does not start")
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

	It("never restores over a database it did not create", func() {
		h = newBootstrapHarness(false)
		h.fake.Seed(h.dbName(), "")
		for i := 0; i < 3; i++ {
			h.reconcile()
		}
		od := h.get()
		Expect(od.Status.Database.ProvisionedBy).To(Equal(odoov1.DatabaseProvisionedByExternal))
		Expect(h.bootstrapRestore()).To(BeNil(), "no restore is created")
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
		Expect(h.job(h.name+"-init")).To(BeNil(), "Odoo does not initialize over it either")
		d := h.deployment()
		Expect(d == nil || *d.Spec.Replicas == 0).To(BeTrue(), "Odoo does not start")
		Expect(od.Status.Phase).To(Equal(odoov1.PhasePending))
		cond := condition(od, odoov1.ConditionDegraded)
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal(odoov1.ReasonBootstrapFailed))
		Expect(cond.Message).To(ContainSubstring(`database "` + h.dbName() + `" already exists`))
		Expect(cond.Message).To(ContainSubstring("Remove spec.bootstrap"))
		Expect(cond.Message).To(ContainSubstring("OdooRestore"))
		Expect(condition(od, odoov1.ConditionReady).Reason).To(Equal(odoov1.ReasonBootstrapFailed))
		Expect(h.fake.Has(h.dbName())).To(BeTrue())
		Expect(h.fake.CallCount("Create")).To(BeZero())

		By("without spec.bootstrap, Odoo runs the existing database")
		od.Spec.Bootstrap = nil
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		Expect(od.Status.CurrentInitJob.Name).NotTo(BeEmpty(), "the usual init Job of an adopted database")
		Expect(h.bootstrapRestore()).To(BeNil())
	})

	It("lets an OdooRestore replace the existing database once spec.bootstrap is removed", func() {
		h = newBootstrapHarness(false)
		h.fake.Seed(h.dbName(), "")
		h.reconcile()
		h.newRestore("replace", func(s *odoov1.OdooRestoreSpec) {
			s.From = &odoov1.BackupSource{Store: h.store, Path: "exports/shop_*.zip"}
		})
		_, od := h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty(), "nothing runs while the bootstrap is refused")
		od.Spec.Bootstrap = nil
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationRestore))
		Expect(od.Status.CurrentOperation.Name).To(Equal(h.name + "-replace"))
		Expect(od.Status.CurrentInitJob.Name).To(BeEmpty(), "the restore goes before the init Job")
	})

	It("fails a bootstrap OdooRestore that never ran when the database turns out to exist", func() {
		h = newBootstrapHarness(false)
		h.fake.Seed(h.dbName(), "")
		od := h.get()
		rs := &odoov1.OdooRestore{
			ObjectMeta: metav1.ObjectMeta{Name: bootstrapRestoreName(h.name), Namespace: ns},
			Spec: odoov1.OdooRestoreSpec{
				OdooDeployment: h.name, From: &odoov1.BackupSource{Store: h.store, Path: "exports/shop_*.zip"},
			},
		}
		Expect(controllerutil.SetOwnerReference(od, rs, k8sClient.Scheme())).To(Succeed())
		Expect(k8sClient.Create(ctx, rs)).To(Succeed())
		for i := 0; i < 3; i++ {
			_, od = h.reconcile()
		}
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty(), "the restore never starts")
		rs = h.bootstrapRestore()
		Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseFailed))
		Expect(rs.Status.Message).To(ContainSubstring("already exists"))
		Expect(rs.Status.CleanedUp).To(BeTrue())

		By("and it does not run later as an ordinary restore")
		od.Spec.Bootstrap = nil
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
		Expect(h.bootstrapRestore().Status.Phase).To(Equal(odoov1.RestorePhaseFailed))
	})

	It("still bootstraps the empty database it created itself", func() {
		h = newBootstrapHarness(false)
		_, od := h.reconcile()
		Expect(od.Status.Database.ProvisionedBy).To(Equal(odoov1.DatabaseProvisionedByOperator))
		Expect(h.fake.Has(h.dbName())).To(BeTrue())
		Expect(h.bootstrapRestore()).NotTo(BeNil())
		_, od = h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationRestore))
		if c := condition(od, odoov1.ConditionDegraded); c != nil {
			Expect(c.Reason).NotTo(Equal(odoov1.ReasonBootstrapFailed))
		}
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
