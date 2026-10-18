package controller

import (
	"sort"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// scheduledBackups are the instance's backups the schedule created, oldest first.
func (h *backupHarness) scheduledBackups() []odoov1.OdooBackup {
	list := &odoov1.OdooBackupList{}
	Expect(k8sClient.List(ctx, list, client.InNamespace(ns), client.MatchingLabels{
		odoov1.LabelOdooDeployment: h.name, odoov1.LabelBackupKind: odoov1.BackupKindScheduled,
	})).To(Succeed())
	// Their names sort by scheduled time.
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list.Items
}

func scheduledNames(bs []odoov1.OdooBackup) []string {
	names := make([]string, 0, len(bs))
	for _, b := range bs {
		names = append(names, b.Name)
	}
	return names
}

func (h *backupHarness) setBackupSpec(mutate func(*odoov1.OdooBackupConfig)) {
	od := h.get()
	mutate(&od.Spec.Backup)
	Expect(k8sClient.Update(ctx, od)).To(Succeed())
}

var _ = Describe("Scheduled backups", func() {
	var (
		h     *backupHarness
		clock time.Time
	)

	at := func(s string) time.Time {
		t, err := time.Parse(time.RFC3339, s)
		Expect(err).NotTo(HaveOccurred())
		return t
	}

	start := func(expr, tz string, now time.Time) {
		h = newBackupHarness()
		clock = now
		h.r.Now = func() time.Time { return clock }
		h.setBackupSpec(func(b *odoov1.OdooBackupConfig) { b.Schedule, b.TimeZone = expr, tz })
	}

	AfterEach(func() {
		if h == nil {
			return
		}
		for _, b := range h.scheduledBackups() {
			_ = k8sClient.Delete(ctx, &b)
		}
		h.cleanupAll()
		h = nil
	})

	It("takes nothing when the schedule is set, then one backup at each scheduled time", func() {
		start("0 3 * * *", "Europe/Berlin", at("2026-03-07T10:00:00Z"))

		res, od := h.reconcile()
		Expect(h.scheduledBackups()).To(BeEmpty(), "a new schedule does not run for times before it")
		st := od.Status.Backup
		Expect(st.Schedule).To(Equal("0 3 * * *"))
		Expect(st.TimeZone).To(Equal("Europe/Berlin"))
		Expect(st.LastScheduleTime.Time).To(BeTemporally("==", clock))
		Expect(st.NextScheduleTime.Time).To(BeTemporally("==", at("2026-03-08T02:00:00Z")), "03:00 in Berlin is 02:00Z in winter")
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		Expect(res.RequeueAfter).To(BeNumerically("<=", 16*time.Hour+time.Second), "wakes for the next time")
		c := meta.FindStatusCondition(od.Status.Conditions, odoov1.ConditionBackupSchedule)
		Expect(c).NotTo(BeNil())
		Expect(c.Status).To(Equal(metav1.ConditionTrue))

		By("one second before the time nothing happens")
		clock = at("2026-03-08T01:59:59Z")
		h.reconcile()
		Expect(h.scheduledBackups()).To(BeEmpty())

		By("at the time the backup is created, named in the schedule's time zone, and starts")
		clock = at("2026-03-08T02:00:01Z")
		_, od = h.reconcile()
		bs := h.scheduledBackups()
		Expect(scheduledNames(bs)).To(Equal([]string{h.name + "-20260308-0300"}))
		Expect(bs[0].Spec.OdooDeployment).To(Equal(h.name))
		Expect(bs[0].Annotations[odoov1.AnnotationScheduledTime]).To(Equal("2026-03-08T02:00:00Z"))
		Expect(od.Status.CurrentOperation.Name).To(Equal(bs[0].Name), "it goes straight into the queue")
		Expect(od.Status.Backup.LastScheduleTime.Time).To(BeTemporally("==", at("2026-03-08T02:00:00Z")))
		Expect(od.Status.Backup.NextScheduleTime.Time).To(BeTemporally("==", at("2026-03-09T02:00:00Z")))

		By("reconciling again creates no second backup")
		h.reconcile()
		Expect(h.scheduledBackups()).To(HaveLen(1))
	})

	It("creates the same backup only once when the status was lost", func() {
		start("0 3 * * *", "UTC", at("2026-03-07T10:00:00Z"))
		h.reconcile()
		clock = at("2026-03-08T03:00:00Z")
		_, od := h.reconcile()
		Expect(h.scheduledBackups()).To(HaveLen(1))

		By("the status forgets the time it handled, as after a crash before the status write")
		od.Status.Backup.LastScheduleTime = &metav1.Time{Time: at("2026-03-07T10:00:00Z")}
		Expect(k8sClient.Status().Update(ctx, od)).To(Succeed())
		h.reconcile()
		Expect(h.scheduledBackups()).To(HaveLen(1))
	})

	It("skips a time while the previous scheduled backup is unfinished, and takes a missed one once", func() {
		start("0 * * * *", "UTC", at("2026-03-07T10:30:00Z"))
		h.reconcile()

		clock = at("2026-03-07T11:00:00Z")
		_, od := h.reconcile()
		Expect(h.scheduledBackups()).To(HaveLen(1))
		job := od.Status.CurrentOperation.Job
		Expect(job).NotTo(BeEmpty())

		By("the next hour comes while it still runs")
		clock = at("2026-03-07T12:00:00Z")
		_, od = h.reconcile()
		Expect(h.scheduledBackups()).To(HaveLen(1), "skipped")
		Expect(od.Status.Backup.LastScheduleTime.Time).To(BeTemporally("==", clock), "the skipped time counts as handled")

		By("it finishes, then the operator is away for five hours")
		h.finishJob(job, &result.Result{OK: true, SnapshotID: "aaaaaaaa"})
		h.reconcile()
		clock = at("2026-03-07T17:20:00Z")
		h.reconcile()
		Expect(scheduledNames(h.scheduledBackups())).To(Equal([]string{h.name + "-20260307-1100", h.name + "-20260307-1700"}),
			"only the latest missed time runs")
	})

	It("waits for an upgrade before the scheduled backup runs", func() {
		start("0 3 * * *", "UTC", at("2026-03-07T10:00:00Z"))
		h.reconcile()
		od := h.get()
		od.Spec.Image = "odoo:b"
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		h.reconcile()
		h.passImageCheck()
		_, od = h.reconcile()
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseUpgrading))

		clock = at("2026-03-08T03:00:00Z")
		_, od = h.reconcile()
		Expect(h.scheduledBackups()).To(HaveLen(1), "created during the upgrade")
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty(), "but not started")
		h.succeedJob(od.Status.CurrentInitJob.Name)
		_, od = h.reconcile()
		Expect(od.Status.CurrentOperation.Name).To(Equal(h.name + "-20260308-0300"))
	})

	It("starts counting again when the schedule changes", func() {
		start("0 3 * * *", "UTC", at("2026-03-07T10:00:00Z"))
		h.reconcile()
		clock = at("2026-03-08T12:00:00Z")
		h.setBackupSpec(func(b *odoov1.OdooBackupConfig) { b.Schedule = "0 4 * * *" })
		_, od := h.reconcile()
		Expect(h.scheduledBackups()).To(BeEmpty(), "the 03:00 the old schedule missed is not taken")
		Expect(od.Status.Backup.NextScheduleTime.Time).To(BeTemporally("==", at("2026-03-09T04:00:00Z")))

		By("removing the schedule clears its status and condition")
		h.setBackupSpec(func(b *odoov1.OdooBackupConfig) { b.Schedule = "" })
		_, od = h.reconcile()
		Expect(od.Status.Backup.Schedule).To(BeEmpty())
		Expect(od.Status.Backup.NextScheduleTime).To(BeNil())
		Expect(meta.FindStatusCondition(od.Status.Conditions, odoov1.ConditionBackupSchedule)).To(BeNil())
	})

	It("reports a schedule it cannot read, and keeps serving", func() {
		start("0 3 * *", "UTC", at("2026-03-07T10:00:00Z"))
		_, od := h.reconcile()
		c := meta.FindStatusCondition(od.Status.Conditions, odoov1.ConditionBackupSchedule)
		Expect(c).NotTo(BeNil())
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Reason).To(Equal(ReasonInvalidSchedule))
		Expect(c.Message).To(ContainSubstring("spec.backup.schedule"))
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseRunning))
		Expect(h.replicas()).To(Equal(int32(1)))

		h.setBackupSpec(func(b *odoov1.OdooBackupConfig) { b.Schedule, b.TimeZone = "0 3 * * *", "Mars/Olympus" })
		_, od = h.reconcile()
		c = meta.FindStatusCondition(od.Status.Conditions, odoov1.ConditionBackupSchedule)
		Expect(c.Message).To(ContainSubstring("unknown time zone"))
	})

	It("is refused without a store", func() {
		od := (&harness{name: "sched-nostore"}).spec(func(od *odoov1.OdooDeployment) {
			od.Spec.Backup.Schedule = "0 3 * * *"
		})
		err := k8sClient.Create(ctx, od)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.backup.schedule needs spec.backup.stores"))
	})

	It("takes no backup before the instance is initialized", func() {
		hh := newHarness()
		DeferCleanup(hh.cleanup)
		clock = at("2026-03-07T10:00:00Z")
		hh.r.Now = func() time.Time { return clock }
		hh.create(func(od *odoov1.OdooDeployment) {
			od.Spec.Backup.Stores = []string{"some-store"}
			od.Spec.Backup.Schedule = "* * * * *"
		})
		_, od := hh.reconcile()
		clock = clock.Add(time.Hour)
		_, od = hh.reconcile()
		Expect(od.Status.InitModulesInstalled).To(BeEmpty())
		Expect(od.Status.Backup.Schedule).To(BeEmpty(), "the schedule has not started")
		list := &odoov1.OdooBackupList{}
		Expect(k8sClient.List(ctx, list, client.InNamespace(ns), client.MatchingLabels{odoov1.LabelOdooDeployment: hh.name})).To(Succeed())
		Expect(list.Items).To(BeEmpty())
	})
})

var _ = Describe("scheduledBackupName", func() {
	It("cuts a long instance name so the name fits a label", func() {
		local := time.Date(2026, 3, 8, 3, 0, 0, 0, time.UTC)
		long := "a-very-long-instance-name-that-goes-on-and-on-and-on-and-on-forever"
		name := scheduledBackupName(long, local)
		Expect(len(name)).To(BeNumerically("<=", 63))
		Expect(name).To(HaveSuffix("-20260308-0300"))
		Expect(scheduledBackupName("shop", local.Add(30*time.Second))).To(Equal("shop-20260308-030030"))
	})
})
