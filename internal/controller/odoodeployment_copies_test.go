package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// secondStore adds a PVC store with its own key and returns its name.
func (h *backupHarness) secondStore() string {
	name := h.name + "-second"
	key := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-key", Namespace: ns},
		Data:       map[string][]byte{"password": []byte("other")},
	}
	store := &odoov1.OdooBackupStore{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: odoov1.OdooBackupStoreSpec{
			PVC: &odoov1.PVCStore{ClaimName: "offsite"},
			EncryptionKeySecret: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: key.Name}, Key: "password",
			},
		},
	}
	h.track(key, store)
	return name
}

func (h *backupHarness) setStoreReady(name string, ready bool) {
	store := &odoov1.OdooBackupStore{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, store)).To(Succeed())
	store.Status.Ready = ready
	Expect(k8sClient.Status().Update(ctx, store)).To(Succeed())
}

func envNames(env []corev1.EnvVar) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		out = append(out, e.Name)
	}
	return out
}

var _ = Describe("Copies to other stores", func() {
	var h *backupHarness
	AfterEach(func() {
		if h != nil {
			h.cleanupAll()
			h = nil
		}
	})

	It("copies each backup in the same Job and records the copies", func() {
		h = newBackupHarness()
		second := h.secondStore()
		h.setBackupSpec(func(b *odoov1.OdooBackupConfig) { b.Stores = []string{h.store, second} })
		h.newBackup("one")
		_, od := h.reconcile()
		job := h.job(od.Status.CurrentOperation.Job)
		Expect(jobArgs(job)).To(ContainSubstring("--copy " + second))
		c := job.Spec.Template.Spec.Containers[0]
		Expect(envNames(c.Env)).To(ContainElements("RESTIC_REPOSITORY", "COPY1_RESTIC_REPOSITORY", "COPY1_RESTIC_PASSWORD_FILE"))
		mounts := make([]string, 0, len(c.VolumeMounts))
		for _, m := range c.VolumeMounts {
			mounts = append(mounts, m.MountPath)
		}
		Expect(mounts).To(ContainElements(layout.StoreMount, layout.StoreMount+"-copy1"))

		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{
			OK: true, SnapshotID: "aaaa1111", Copies: []result.Copy{{Store: second, SnapshotID: "bbbb2222"}},
		})
		h.reconcile()
		b := h.backup("one")
		Expect(b.Status.Phase).To(Equal(odoov1.BackupPhaseCompleted))
		Expect(b.Status.Copies).To(Equal([]odoov1.BackupCopy{{Store: second, SnapshotID: "bbbb2222"}}))
	})

	It("is partially completed when a copy failed or its store cannot be used", func() {
		h = newBackupHarness()
		second := h.secondStore()
		h.setBackupSpec(func(b *odoov1.OdooBackupConfig) { b.Stores = []string{h.store, second, "nowhere"} })
		h.newBackup("one")
		_, od := h.reconcile()
		job := h.job(od.Status.CurrentOperation.Job)
		Expect(jobArgs(job)).NotTo(ContainSubstring("--copy nowhere"))
		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{
			OK: true, SnapshotID: "aaaa1111", Copies: []result.Copy{{Store: second, Error: "connection refused"}},
		})
		_, od = h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
		b := h.backup("one")
		Expect(b.Status.Phase).To(Equal(odoov1.BackupPhasePartiallyCompleted))
		Expect(b.Status.SnapshotID).To(Equal("aaaa1111"))
		Expect(b.Status.Message).To(ContainSubstring("the copy to nowhere, " + second + " failed"))
		Expect(b.Status.Copies).To(HaveLen(2))
		Expect(b.Status.Copies[0].Error).To(ContainSubstring("does not exist"))
		Expect(b.Status.Copies[1].Error).To(Equal("connection refused"))

		By("a partially completed backup can be restored")
		h.newRestore("r", func(s *odoov1.OdooRestoreSpec) { s.Backup = h.name + "-one" })
		_, od = h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationRestore))
	})

	It("restores the copy when the backup's own store is not ready", func() {
		h = newBackupHarness()
		second := h.secondStore()
		h.setBackupSpec(func(b *odoov1.OdooBackupConfig) { b.Stores = []string{h.store, second} })
		b := h.newBackup("one")
		b.Status = odoov1.OdooBackupStatus{
			Phase: odoov1.BackupPhaseCompleted, Store: h.store, SnapshotID: "aaaa1111",
			Copies: []odoov1.BackupCopy{{Store: second, SnapshotID: "bbbb2222"}},
		}
		Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
		h.setStoreReady(h.store, false)
		h.setStoreReady(second, true)

		h.newRestore("r", func(s *odoov1.OdooRestoreSpec) { s.Backup = b.Name })
		_, od := h.reconcile()
		Expect(jobArgs(h.job(od.Status.CurrentOperation.Job))).To(ContainSubstring("--snapshot bbbb2222"))
		Expect(h.restore("r").Status.Store).To(Equal(second))
	})

	It("restores from the backup's own store when it is ready", func() {
		h = newBackupHarness()
		second := h.secondStore()
		b := h.newBackup("one")
		b.Status = odoov1.OdooBackupStatus{
			Phase: odoov1.BackupPhaseCompleted, Store: h.store, SnapshotID: "aaaa1111",
			Copies: []odoov1.BackupCopy{{Store: second, SnapshotID: "bbbb2222"}},
		}
		Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
		h.setStoreReady(h.store, true)
		h.setStoreReady(second, true)
		h.newRestore("r", func(s *odoov1.OdooRestoreSpec) { s.Backup = b.Name })
		_, od := h.reconcile()
		Expect(jobArgs(h.job(od.Status.CurrentOperation.Job))).To(ContainSubstring("--snapshot aaaa1111"))
		Expect(h.restore("r").Status.Store).To(Equal(h.store))
	})
})
