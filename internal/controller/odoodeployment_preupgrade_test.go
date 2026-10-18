package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
	"github.com/MohanadAbugharbia/odoo-operator/internal/maintenancepage"
)

// preUpgradeBackups lists the instance's pre-upgrade backups and deletes
// them after the test.
func (h *backupHarness) preUpgradeBackups() []odoov1.OdooBackup {
	list := &odoov1.OdooBackupList{}
	Expect(k8sClient.List(ctx, list, client.InNamespace(ns), client.MatchingLabels{
		odoov1.LabelOdooDeployment: h.name, odoov1.LabelBackupKind: odoov1.BackupKindPreUpgrade,
	})).To(Succeed())
	for i := range list.Items {
		b := list.Items[i]
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, &b) })
	}
	return list.Items
}

// setImage changes spec.image, as a user upgrading would.
func (h *backupHarness) setImage(image string) {
	od := h.get()
	od.Spec.Image = image
	Expect(k8sClient.Update(ctx, od)).To(Succeed())
}

func beforeUpgrade(mode odoov1.BeforeUpgrade) func(*odoov1.OdooDeployment) {
	return func(od *odoov1.OdooDeployment) { od.Spec.Backup.BeforeUpgrade = mode }
}

var _ = Describe("Backups before upgrades", func() {
	var h *backupHarness

	AfterEach(func() {
		if h != nil {
			h.cleanupAll()
		}
	})

	// startPreUpgradeBackup changes the image and drives the instance to its
	// pre-upgrade backup's running Job, checking the old image serves until
	// then.
	startPreUpgradeBackup := func() (*odoov1.OdooDeployment, odoov1.OdooBackup) {
		h.setImage("odoo:b")
		h.reconcile()
		_, od := h.passImageCheck()
		backups := h.preUpgradeBackups()
		Expect(backups).To(HaveLen(1))
		b := backups[0]
		Expect(b.Name).To(HavePrefix(h.name + "-pre-upgrade-"))
		Expect(b.Annotations[odoov1.AnnotationUpgradeTarget]).To(Equal("odoo:b "))
		Expect(b.Spec.OdooDeployment).To(Equal(h.name))
		Expect(od.Status.PreUpgradeBackup).To(Equal(odoov1.PreUpgradeBackupStatus{Name: b.Name, Target: "odoo:b "}))
		Expect(od.Status.CurrentInitJob.Name).To(BeEmpty(), "no upgrade Job before the backup")
		Expect(od.Status.AppliedImage).To(Equal("odoo:a"))

		_, od = h.reconcile()
		op := od.Status.CurrentOperation
		Expect(op.Kind).To(Equal(odoov1.OperationBackup))
		Expect(op.Name).To(Equal(b.Name))
		Expect(op.StoppedOdoo).To(BeTrue(), "a pre-upgrade backup always stops Odoo")
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseBackingUp))
		Expect(h.replicas()).To(Equal(int32(0)))
		job := h.job(op.Job)
		Expect(jobArgs(job)).To(ContainSubstring("--image odoo:a"), "the backup records the image it was taken with")
		Expect(jobArgs(job)).To(ContainSubstring("--consistency Offline"))
		return od, b
	}

	It("is not taken when beforeUpgrade is Off", func() {
		h = newBackupHarness()
		h.setImage("odoo:b")
		h.reconcile()
		_, od := h.passImageCheck()
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseUpgrading))
		Expect(h.preUpgradeBackups()).To(BeEmpty())
	})

	It("is taken with Odoo stopped, then the upgrade runs", func() {
		h = newBackupHarness(beforeUpgrade(odoov1.BeforeUpgradeRequired), func(od *odoov1.OdooDeployment) {
			od.Spec.MaintenancePage = odoov1.MaintenancePageConfig{Enabled: true, Title: "My Shop"}
		})
		od, b := startPreUpgradeBackup()

		By("the maintenance page says an upgrade follows the backup")
		h.setAvailable(0)
		h.reconcile()
		snap := h.maintenanceStatus()
		Expect(snap.Status.State).To(Equal(maintenancepage.StateBackingUp))
		Expect(snap.Status.Job).To(Equal(odoov1.JobKindUpgrade))
		Expect(snap.Status.Version).To(Equal("b"))
		Expect(snap.Status.StartedAt).NotTo(BeNil())
		Expect(*h.maintenancePage().Spec.Replicas).To(Equal(int32(1)))

		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: true, SnapshotID: "abcdef0123456789"})
		_, od = h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseUpgrading))
		Expect(od.Status.CurrentInitJob.Name).NotTo(BeEmpty())
		Expect(h.replicas()).To(Equal(int32(0)), "Odoo stays stopped between the backup and the upgrade")

		h.succeedJob(od.Status.CurrentInitJob.Name)
		_, od = h.reconcile()
		Expect(od.Status.AppliedImage).To(Equal("odoo:b"))
		Expect(od.Status.PreUpgradeBackup).To(BeZero(), "cleared once the upgrade is done")
		fresh := h.preUpgradeBackups()
		Expect(fresh).To(HaveLen(1), "no second backup")
		Expect(fresh[0].Name).To(Equal(b.Name))
		Expect(fresh[0].Status.Phase).To(Equal(odoov1.BackupPhaseCompleted))
		Expect(fresh[0].Status.Image).To(Equal("odoo:a"))
	})

	It("keeps the old image when Required and the backup fails, and tries again once deleted", func() {
		h = newBackupHarness(beforeUpgrade(odoov1.BeforeUpgradeRequired))
		od, b := startPreUpgradeBackup()
		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: false, Step: result.StepUpload, Error: "no space left"})
		_, od = h.reconcile()
		Expect(od.Status.CurrentInitJob.Name).To(BeEmpty(), "no upgrade without the backup")
		Expect(od.Status.AppliedImage).To(Equal("odoo:a"))
		Expect(h.replicas()).To(Equal(int32(1)), "the old image serves again")
		Expect(h.deployment().Spec.Template.Spec.Containers[0].Image).To(Equal("odoo:a"))
		deg := condition(od, odoov1.ConditionDegraded)
		Expect(deg).NotTo(BeNil())
		Expect(deg.Status).To(Equal(metav1.ConditionTrue))
		Expect(deg.Reason).To(Equal(odoov1.ReasonPreUpgradeBackupFailed))
		Expect(deg.Message).To(ContainSubstring(b.Name))
		Expect(deg.Message).To(ContainSubstring("no space left"))

		By("another reconcile changes nothing")
		_, od = h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
		Expect(h.preUpgradeBackups()).To(HaveLen(1))

		By("deleting the failed backup takes a new one")
		Expect(k8sClient.Delete(ctx, &b)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: b.Name}, &odoov1.OdooBackup{}))
		}).Should(BeTrue())
		// A new name needs a new second.
		h.r.Now = func() time.Time { return time.Now().Add(time.Hour) }
		DeferCleanup(func() { h.r.Now = nil })
		_, od = h.reconcile()
		backups := h.preUpgradeBackups()
		Expect(backups).To(HaveLen(1))
		Expect(backups[0].Name).NotTo(Equal(b.Name))
		Expect(od.Status.PreUpgradeBackup.Name).To(Equal(backups[0].Name))
	})

	It("upgrades anyway when BestEffort and the backup fails", func() {
		h = newBackupHarness(beforeUpgrade(odoov1.BeforeUpgradeBestEffort))
		od, _ := startPreUpgradeBackup()
		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: false, Step: result.StepUpload, Error: "no space left"})
		_, od = h.reconcile()
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseUpgrading))
		Expect(od.Status.CurrentInitJob.Name).NotTo(BeEmpty())
	})

	It("follows the backup it started after an operator restart", func() {
		h = newBackupHarness(beforeUpgrade(odoov1.BeforeUpgradeRequired))
		_, b := startPreUpgradeBackup()
		// A new reconciler, as after a restart, finds the backup in the status.
		h.r = &OdooDeploymentReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient, DB: h.fake, Recorder: h.recorder,
			MaintenancePageImage: "operator:test", BackupImage: h.r.BackupImage, Version: h.r.Version,
		}
		_, od := h.reconcile()
		Expect(od.Status.CurrentOperation.Name).To(Equal(b.Name))
		Expect(h.preUpgradeBackups()).To(HaveLen(1))
		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: true, SnapshotID: "abcdef0123456789"})
		_, od = h.reconcile()
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseUpgrading))
	})

	It("is taken for an upgrade token too", func() {
		h = newBackupHarness(beforeUpgrade(odoov1.BeforeUpgradeRequired))
		od := h.get()
		od.Spec.Upgrade.Token = "again"
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		backups := h.preUpgradeBackups()
		Expect(backups).To(HaveLen(1))
		Expect(backups[0].Annotations[odoov1.AnnotationUpgradeTarget]).To(Equal("odoo:a again"))
		Expect(od.Status.CurrentInitJob.Name).To(BeEmpty())
	})
})
