package controller

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dto "github.com/prometheus/client_model/go"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// metricSample returns the sample of family name whose labels include
// labels, and whether it exists.
func metricSample(name string, labels map[string]string) (*dto.Metric, bool) {
	families, err := ctrlmetrics.Registry.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
	next:
		for _, m := range f.GetMetric() {
			have := map[string]string{}
			for _, l := range m.GetLabel() {
				have[l.GetName()] = l.GetValue()
			}
			for k, v := range labels {
				if have[k] != v {
					continue next
				}
			}
			return m, true
		}
	}
	return nil, false
}

func counterValue(name string, labels map[string]string) float64 {
	m, ok := metricSample(name, labels)
	Expect(ok).To(BeTrue(), fmt.Sprintf("%s%v is not on /metrics", name, labels))
	return m.GetCounter().GetValue()
}

func jobDurationCount(labels map[string]string) uint64 {
	m, ok := metricSample("odoo_operator_job_duration_seconds", labels)
	if !ok {
		return 0
	}
	return m.GetHistogram().GetSampleCount()
}

var _ = Describe("OdooDeployment metrics", func() {
	var h *harness
	BeforeEach(func() { h = newHarness() })
	AfterEach(func() { h.cleanup() })

	jobs := func(kind, result string) float64 {
		return counterValue("odoo_operator_jobs_total",
			map[string]string{"namespace": ns, "name": h.name, "kind": kind, "result": result})
	}

	It("counts the init and upgrade Jobs that succeed, starting from zero", func() {
		h.create()
		h.reconcile()
		Expect(jobs(odoov1.JobKindInit, "succeeded")).To(BeZero(), "the series exists before the first event")
		Expect(jobs(odoov1.JobKindUpgrade, "failed")).To(BeZero())

		upgrades := jobDurationCount(map[string]string{"kind": "upgrade", "result": "succeeded"})
		h.succeedJob(h.name + "-init")
		_, od := h.reconcile()
		Expect(jobs(odoov1.JobKindInit, "succeeded")).To(Equal(1.0))

		od.Spec.Image = "odoo:b"
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		h.reconcile()
		_, od = h.passImageCheck()
		h.succeedJob(od.Status.CurrentInitJob.Name)
		h.reconcile()
		h.reconcile()
		Expect(jobs(odoov1.JobKindUpgrade, "succeeded")).To(Equal(1.0))
		Expect(jobDurationCount(map[string]string{"kind": "upgrade", "result": "succeeded"})).
			To(Equal(upgrades + 1))
	})

	It("counts a failed Job once, however often the Failed phase is reconciled", func() {
		h.create()
		h.reconcile()
		failed := jobDurationCount(map[string]string{"kind": "init", "result": "failed"})
		h.failJob(h.name + "-init")
		for range 3 {
			_, od := h.reconcile()
			Expect(od.Status.Phase).To(Equal(odoov1.PhaseFailed))
		}
		Expect(jobs(odoov1.JobKindInit, "failed")).To(Equal(1.0))
		Expect(jobDurationCount(map[string]string{"kind": "init", "result": "failed"})).
			To(Equal(failed+1), "the duration ends at the Job's Failed condition")
	})

	It("counts an image that cannot be pulled once while the pull keeps failing", func() {
		h.create()
		od := h.bootToRunning()
		od.Spec.Image = "odoo:b"
		Expect(k8sClient.Update(ctx, od)).To(Succeed())
		_, od = h.reconcile()
		h.podStuckPulling(od.ImageCheckJobName(), "odoo:b")
		for range 3 {
			_, od = h.reconcile()
			Expect(condition(od, odoov1.ConditionDegraded).Reason).To(Equal(odoov1.ReasonImagePullFailed))
		}
		Expect(counterValue("odoo_operator_image_pull_failures_total",
			map[string]string{"namespace": ns, "name": h.name})).To(Equal(1.0))
	})

	It("counts a failing drop once and forgets the OdooDeployment once it is gone", func() {
		h.create()
		od := h.bootToRunning()
		dropFailed := map[string]string{"operation": "drop", "result": "failed"}
		dropped := map[string]string{"operation": "drop", "result": "succeeded"}
		before, beforeDropped := counterValue("odoo_operator_database_operations_total", dropFailed),
			counterValue("odoo_operator_database_operations_total", dropped)

		h.fake.DropErr = fmt.Errorf("connection refused")
		Expect(k8sClient.Delete(ctx, od)).To(Succeed())
		h.reconcile()
		h.reconcile()
		Expect(counterValue("odoo_operator_database_operations_total", dropFailed)).To(Equal(before + 1))

		h.fake.DropErr = nil
		_, od = h.reconcile()
		Expect(od).To(BeNil())
		Expect(counterValue("odoo_operator_database_operations_total", dropped)).To(Equal(beforeDropped + 1))

		By("the reconcile that finds it gone drops its series")
		h.reconcile()
		_, ok := metricSample("odoo_operator_jobs_total", map[string]string{"namespace": ns, "name": h.name})
		Expect(ok).To(BeFalse())
	})
})
