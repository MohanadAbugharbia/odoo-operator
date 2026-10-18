package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// doneBackup is a finished backup holding snapshot id in store.
func (h *backupHarness) doneBackup(name, id, store string, keep bool) {
	b := h.newBackup(name)
	if keep {
		b.Spec.Keep = true
		Expect(k8sClient.Update(ctx, b)).To(Succeed())
	}
	b.Status = odoov1.OdooBackupStatus{
		Phase: odoov1.BackupPhaseCompleted, Store: store, SnapshotID: id,
		Repository: layout.Repository(ns, h.name),
	}
	Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
}

func (h *backupHarness) backupGone(name string) bool {
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: h.name + "-" + name}, &odoov1.OdooBackup{})
	return apierrors.IsNotFound(err)
}

var _ = Describe("Retention", func() {
	var h *backupHarness
	AfterEach(func() {
		if h != nil {
			h.cleanupAll()
			h = nil
		}
	})

	It("passes the rules, with their defaults, and what to protect to the backup Job", func() {
		h = newBackupHarness(func(od *odoov1.OdooDeployment) {
			od.Spec.Backup.TimeZone = "Asia/Tokyo"
			od.Spec.Backup.Retention = odoov1.OdooBackupRetention{Hourly: ptr.To(int32(24)), Within: "14d", Manual: ptr.To(int32(3))}
		})
		h.doneBackup("kept", "aaaa1111", h.store, true)
		h.doneBackup("plain", "bbbb2222", h.store, false)
		h.newRestore("later", func(s *odoov1.OdooRestoreSpec) { s.Backup = h.name + "-plain" })
		h.newRestore("by-id", func(s *odoov1.OdooRestoreSpec) { s.SnapshotID = "cccc3333" })

		b := h.newBackup("now")
		// Restores go first; finish them as failed so the backup can run.
		for _, name := range []string{"later", "by-id"} {
			rs := h.restore(name)
			rs.Status.Phase = odoov1.RestorePhaseFailed
			Expect(k8sClient.Status().Update(ctx, rs)).To(Succeed())
		}
		_, od := h.reconcile()
		Expect(od.Status.CurrentOperation.Name).To(Equal(b.Name))
		args := jobArgs(h.job(od.Status.CurrentOperation.Job))
		Expect(args).To(ContainSubstring("--retention --time-zone Asia/Tokyo"))
		Expect(args).To(ContainSubstring("--keep-hourly 24 --keep-daily 7 --keep-weekly 4 --keep-monthly 12 --keep-manual 3"))
		Expect(args).To(ContainSubstring("--keep-within 14d"))
		Expect(args).To(ContainSubstring("--protect-backup " + h.name + "-kept"))
		Expect(args).NotTo(ContainSubstring("--protect-backup " + h.name + "-plain"))
		Expect(args).NotTo(ContainSubstring("--protect-snapshot"), "failed restores need nothing")
	})

	It("protects the snapshots of restores still to run", func() {
		h = newBackupHarness()
		h.doneBackup("plain", "bbbb2222", h.store, false)
		rs := h.newRestore("later", func(s *odoov1.OdooRestoreSpec) { s.Backup = h.name + "-plain" })
		ret := backupRetention(h.get(), instanceWork{
			backups:  []odoov1.OdooBackup{*h.backup("plain")},
			restores: []odoov1.OdooRestore{*rs},
		})
		Expect(ret.ProtectSnapshots).To(Equal([]string{"bbbb2222"}))
	})

	It("is off with keepAll", func() {
		h = newBackupHarness(func(od *odoov1.OdooDeployment) { od.Spec.Backup.Retention.KeepAll = true })
		h.newBackup("now")
		_, od := h.reconcile()
		Expect(jobArgs(h.job(od.Status.CurrentOperation.Job))).NotTo(ContainSubstring("--retention"))
	})

	It("deletes the backups whose snapshot the Job forgot, in that store only", func() {
		h = newBackupHarness()
		h.doneBackup("old1", "1111aaaa", h.store, false)
		h.doneBackup("old2", "2222bbbb", h.store, false)
		h.doneBackup("elsewhere", "1111aaaa", "other-store", false)
		h.doneBackup("young", "3333cccc", h.store, false)
		h.newBackup("now")
		_, od := h.reconcile()
		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{
			OK: true, SnapshotID: "4444dddd", Forgotten: []string{"1111aaaa", "2222bbbb"},
		})
		_, od = h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
		Expect(h.backupGone("old1")).To(BeTrue())
		Expect(h.backupGone("old2")).To(BeTrue())
		Expect(h.backupGone("elsewhere")).To(BeFalse(), "same ID, other store")
		Expect(h.backupGone("young")).To(BeFalse())
		Expect(h.backup("now").Status.Phase).To(Equal(odoov1.BackupPhaseCompleted))
	})

	It("keeps the backup completed when retention failed, with a warning", func() {
		h = newBackupHarness()
		h.newBackup("now")
		_, od := h.reconcile()
		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{
			OK: true, SnapshotID: "4444dddd", RetentionError: "restic forget: exit 1: boom",
		})
		h.reconcile()
		b := h.backup("now")
		Expect(b.Status.Phase).To(Equal(odoov1.BackupPhaseCompleted))
		Expect(b.Status.Warnings).To(ConsistOf("retention did not run: restic forget: exit 1: boom"))
	})
})
