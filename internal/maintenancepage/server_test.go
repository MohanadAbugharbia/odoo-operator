package maintenancepage

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

type fakeSource struct {
	od  *odoov1.OdooDeployment
	err error
}

func (f *fakeSource) Get() (Snapshot, bool, error) {
	if f.od == nil {
		return Snapshot{}, false, f.err
	}
	return SnapshotOf(f.od), true, f.err
}

var clock = time.Date(2026, 10, 9, 21, 20, 0, 0, time.UTC)

func upgrading() *odoov1.OdooDeployment {
	started := metav1.NewTime(clock.Add(-100 * time.Second))
	return &odoov1.OdooDeployment{
		Spec: odoov1.OdooDeploymentSpec{MaintenancePage: odoov1.MaintenancePageConfig{
			Enabled:           true,
			Title:             "Ababiel",
			TitleTranslations: map[string]odoov1.MaintenancePageText{"ar": "أبابيل"},
			AccentColor:       "#f2800d",
		}},
		Status: odoov1.OdooDeploymentStatus{
			Phase:                   odoov1.PhaseUpgrading,
			InitModulesInstalled:    []string{"base"},
			LastMaintenanceDuration: &metav1.Duration{Duration: 170 * time.Second},
			CurrentInitJob: odoov1.MaintenanceJobStatus{
				Name: "pr-1-upgrade-3f2a9c1e", Kind: odoov1.JobKindUpgrade,
				Image: "ghcr.io/acme/odoo:sha-4be91c2", StartedAt: &started,
			},
		},
	}
}

func serve(t *testing.T, src Source, method, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	(&Handler{Source: src, Now: func() time.Time { return clock }}).ServeHTTP(rec, req)
	return rec
}

var navigate = map[string]string{"Sec-Fetch-Mode": "navigate", "Accept": "text/html"}

func TestNavigationGetsThePageWith503(t *testing.T) {
	rec := serve(t, &fakeSource{od: upgrading()}, http.MethodGet, "/pos/ui?config_id=1", navigate)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
	for k, want := range map[string]string{
		"Content-Type":  "text/html; charset=utf-8",
		"Cache-Control": "no-store",
		"Retry-After":   "10",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("missing CSP: %q", rec.Header().Get("Content-Security-Policy"))
	}
	body := rec.Body.String()
	for _, want := range []string{`lang="en"`, `dir="ltr"`, "<title>Ababiel</title>", "--accent: #f2800d", "Updating Ababiel", `"sha-4be91c2"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "pr-1-upgrade-3f2a9c1e") {
		t.Error("the page must not name the Job")
	}
}

func TestEverythingElseGetsAnEmpty503(t *testing.T) {
	for name, tc := range map[string]struct {
		method, path string
		header       map[string]string
	}{
		"json rpc":        {http.MethodPost, "/web/dataset/call_kw/res.partner/read", map[string]string{"Sec-Fetch-Mode": "cors", "Accept": "*/*"}},
		"health":          {http.MethodGet, "/web/health", map[string]string{"Sec-Fetch-Mode": "cors"}},
		"asset":           {http.MethodGet, "/web/assets/1/web.assets_web.min.js", map[string]string{"Sec-Fetch-Mode": "no-cors"}},
		"service worker":  {http.MethodGet, "/hub", map[string]string{"Sec-Fetch-Mode": "same-origin", "Accept": "text/html"}},
		"curl, no accept": {http.MethodGet, "/", nil},
	} {
		t.Run(name, func(t *testing.T) {
			rec := serve(t, &fakeSource{od: upgrading()}, tc.method, tc.path, tc.header)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status %d, want 503", rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("body %q, want empty", rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Error("missing Cache-Control: no-store")
			}
		})
	}
}

func TestArabicFromAcceptLanguage(t *testing.T) {
	h := map[string]string{"Sec-Fetch-Mode": "navigate", "Accept-Language": "ar-PS,ar;q=0.9,en-US;q=0.8"}
	body := serve(t, &fakeSource{od: upgrading()}, http.MethodGet, "/odoo", h).Body.String()
	for _, want := range []string{`lang="ar"`, `dir="rtl"`, "<title>أبابيل</title>", "جاري تحديث أبابيل"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestPickLanguage(t *testing.T) {
	for header, want := range map[string]string{
		"":                         "en",
		"de-DE,de;q=0.9":           "en",
		"ar":                       "ar",
		"en-US,en;q=0.9,ar;q=0.8":  "en",
		"fr;q=1,ar;q=0.5,en;q=0.4": "ar",
		"ar;q=0,en":                "en",
		"ar_001":                   "ar",
	} {
		if got := pickLanguage(header); got != want {
			t.Errorf("pickLanguage(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestStatusJSON(t *testing.T) {
	rec := serve(t, &fakeSource{od: upgrading()}, http.MethodGet, "/__maintenance/status.json", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, cache %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.State != StateUpdating || st.Job != "upgrade" || st.ExpectedSeconds != 170 || st.Version != "sha-4be91c2" {
		t.Errorf("unexpected status %+v", st)
	}
	if st.StartedAt == nil || !st.StartedAt.Equal(clock.Add(-100*time.Second)) || !st.Now.Equal(clock) {
		t.Errorf("unexpected times %+v", st)
	}
}

func TestStatusOf(t *testing.T) {
	failed := upgrading()
	failed.Status.Phase = odoov1.PhaseFailed
	firstFail := &odoov1.OdooDeployment{Status: odoov1.OdooDeploymentStatus{Phase: odoov1.PhaseFailed}}
	installing := &odoov1.OdooDeployment{Status: odoov1.OdooDeploymentStatus{Phase: odoov1.PhaseInitializing}}
	running := upgrading()
	running.Status.Phase = odoov1.PhaseRunning
	running.Status.CurrentInitJob = odoov1.MaintenanceJobStatus{}
	pendingFirst := &odoov1.OdooDeployment{Status: odoov1.OdooDeploymentStatus{Phase: odoov1.PhasePending}}

	for name, tc := range map[string]struct {
		od         *odoov1.OdooDeployment
		state, job string
	}{
		"pending, new":    {pendingFirst, StatePreparing, ""},
		"init job":        {installing, StateInstalling, "init"},
		"upgrade job":     {upgrading(), StateUpdating, "upgrade"},
		"starting":        {running, StateStarting, ""},
		"upgrade failed":  {failed, StateFailed, "upgrade"},
		"first boot fail": {firstFail, StateFailed, "init"},
	} {
		st := StatusOf(tc.od)
		if st.State != tc.state || st.Job != tc.job {
			t.Errorf("%s: got %s/%s, want %s/%s", name, st.State, st.Job, tc.state, tc.job)
		}
		if tc.state == StateFailed && st.StartedAt != nil {
			t.Errorf("%s: a failed state carries no start time", name)
		}
	}
}

func TestStaleStatusKeepsTheLastState(t *testing.T) {
	rec := serve(t, &fakeSource{od: upgrading(), err: errors.New("timeout")}, http.MethodGet, "/__maintenance/status.json", nil)
	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.State != StateUpdating || !st.Stale {
		t.Errorf("got %+v, want the last state marked stale", st)
	}
}

func TestNothingReadYet(t *testing.T) {
	rec := serve(t, &fakeSource{err: errors.New("no such file")}, http.MethodGet, "/__maintenance/status.json", nil)
	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.State != StateUnavailable || st.Stale {
		t.Errorf("got %+v, want unavailable", st)
	}
}

func TestFileSource(t *testing.T) {
	path := t.TempDir() + "/status.json"
	src := NewFileSource(path)
	if _, ok, err := src.Get(); ok || err == nil {
		t.Fatal("a missing file must not read as a snapshot")
	}
	data, _ := json.Marshal(SnapshotOf(upgrading()))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	snap, ok, err := src.Get()
	if !ok || err != nil || snap.Status.State != StateUpdating || snap.Page.Title != "Ababiel" {
		t.Fatalf("got %+v ok=%v err=%v", snap, ok, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, ok, err = src.Get()
	if !ok || err == nil || snap.Status.State != StateUpdating {
		t.Errorf("a bad read must keep the last snapshot, got %+v ok=%v err=%v", snap, ok, err)
	}
}

func TestSnapshotHoldsNoClock(t *testing.T) {
	a, _ := json.Marshal(SnapshotOf(upgrading()))
	time.Sleep(time.Millisecond)
	b, _ := json.Marshal(SnapshotOf(upgrading()))
	if string(a) != string(b) {
		t.Error("the snapshot must only change when the status does")
	}
}

func TestPageWithoutAStatus(t *testing.T) {
	rec := serve(t, &fakeSource{err: errors.New("no such file")}, http.MethodGet, "/", navigate)
	body := rec.Body.String()
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(body, "<title>Odoo</title>") ||
		!strings.Contains(body, "--accent: #714b67") {
		t.Errorf("want the default branding, got %d", rec.Code)
	}
}

func TestLogoIsOnlyEmbeddedAsADataURI(t *testing.T) {
	od := upgrading()
	od.Spec.MaintenancePage.Logo = "data:image/png;base64,iVBORw0KGgo="
	body := serve(t, &fakeSource{od: od}, http.MethodGet, "/", navigate).Body.String()
	if !strings.Contains(body, `<img src="data:image/png;base64,iVBORw0KGgo="`) {
		t.Error("the logo is missing")
	}
	od.Spec.MaintenancePage.Logo = "javascript:alert(1)"
	od.Spec.MaintenancePage.AccentColor = "red;}</style><script>alert(1)</script>"
	body = serve(t, &fakeSource{od: od}, http.MethodGet, "/", navigate).Body.String()
	if strings.Contains(body, "javascript:") || strings.Contains(body, "alert(1)") {
		t.Error("an invalid logo or accent reached the page")
	}
}

func TestHealthz(t *testing.T) {
	rec := serve(t, &fakeSource{err: errors.New("down")}, http.MethodGet, "/__maintenance/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestImageTag(t *testing.T) {
	for image, want := range map[string]string{
		"odoo:18":                        "18",
		"ghcr.io/acme/odoo:sha-4be91c2":  "sha-4be91c2",
		"registry:5000/odoo":             "",
		"ghcr.io/acme/odoo@sha256:abcd":  "",
		"registry:5000/team/odoo:pr-161": "pr-161",
	} {
		if got := imageTag(image); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", image, got, want)
		}
	}
}
