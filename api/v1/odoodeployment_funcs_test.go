package v1

import (
	"regexp"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// minimalOdooDeployment returns an OdooDeployment with just enough fields
// set to render templates without panicking.
// OdooCommand is set to "odoo" to match the kubebuilder API-server default;
// unit tests construct structs directly so the default is not applied automatically.
func minimalOdooDeployment(specModules, installedModules []string) *OdooDeployment {
	return &OdooDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-odoo",
			Namespace: "default",
		},
		Spec: OdooDeploymentSpec{
			Image:       "odoo:18",
			OdooCommand: []string{"odoo"},
			Config: OdooConfig{
				DataDir: "/var/lib/odoo",
			},
			Modules: specModules,
		},
		Status: OdooDeploymentStatus{
			OdooDataPvcName:      "test-odoo",
			OdooConfigSecretName: "test-odoo-config",
			InitModulesInstalled: installedModules,
		},
	}
}

func testConn() DatabaseConnectionDetails {
	return DatabaseConnectionDetails{Host: "localhost", Port: 5432, User: "odoo", Password: "dbpass", Name: "odoo", MaxConn: 20}
}

func lines(config string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(config, "\n") {
		k, v, ok := strings.Cut(l, " = ")
		if ok {
			out[k] = v
		}
	}
	return out
}

func TestGetSerializedOdooConfig_Defaults(t *testing.T) {
	cfg := &OdooConfig{DataDir: "/var/lib/odoo"}
	got := lines(cfg.GetSerializedOdooConfig("adminpass", testConn()))

	want := map[string]string{
		"admin_passwd":     "adminpass",
		"data_dir":         "/var/lib/odoo",
		"db_host":          "localhost",
		"db_port":          "5432",
		"db_user":          "odoo",
		"db_password":      "dbpass",
		"db_maxconn":       "20",
		"db_name":          "odoo",
		"db_sslmode":       "disable",
		"list_db":          "False",
		"dbfilter":         "^odoo$",
		"without_demo":     "true",
		"proxy_mode":       "true",
		"workers":          "2",
		"max_cron_threads": "1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	for _, absent := range []string{"server_wide_modules", "addons_path"} {
		if _, ok := got[absent]; ok {
			t.Errorf("unexpected %s line", absent)
		}
	}
}

func TestGetSerializedOdooConfig_Order(t *testing.T) {
	cfg := &OdooConfig{DataDir: "/var/lib/odoo", ExtraOptions: map[string]OdooOptionValue{"b_opt": "2", "a_opt": "1"}}
	got := cfg.GetSerializedOdooConfig("x", testConn())
	if !strings.HasPrefix(got, "[options]\nadmin_passwd = x\ndata_dir = /var/lib/odoo\ndb_host = localhost\n") {
		t.Errorf("unexpected prefix:\n%s", got)
	}
	if !strings.HasSuffix(got, "max_cron_threads = 1\na_opt = 1\nb_opt = 2\n") {
		t.Errorf("extraOptions must come last, sorted by key:\n%s", got)
	}
}

func TestGetSerializedOdooConfig_SSLMode(t *testing.T) {
	cfg := &OdooConfig{DataDir: "/var/lib/odoo"}
	conn := testConn()
	if got := lines(cfg.GetSerializedOdooConfig("x", conn)); got["db_sslmode"] != "disable" {
		t.Errorf("ssl=false → db_sslmode = %q, want disable", got["db_sslmode"])
	}
	conn.SSL = true
	if got := lines(cfg.GetSerializedOdooConfig("x", conn)); got["db_sslmode"] != "require" {
		t.Errorf("ssl=true → db_sslmode = %q, want require", got["db_sslmode"])
	}
}

func TestGetSerializedOdooConfig_ListDbAndDbFilter(t *testing.T) {
	f, tr := false, true
	conn := testConn()
	conn.Name = "pal_odoo_pr_12"

	tests := []struct {
		name         string
		cfg          OdooConfig
		wantListDb   string
		wantDbFilter string
		wantNoFilter bool
	}{
		{name: "nil listDb derives the filter", cfg: OdooConfig{}, wantListDb: "False", wantDbFilter: "^pal_odoo_pr_12$"},
		{name: "explicit false derives the filter", cfg: OdooConfig{ListDb: &f}, wantListDb: "False", wantDbFilter: "^pal_odoo_pr_12$"},
		{name: "explicit dbFilter wins", cfg: OdooConfig{ListDb: &f, DbFilter: "^%h$"}, wantListDb: "False", wantDbFilter: "^%h$"},
		{name: "opt-out without filter", cfg: OdooConfig{ListDb: &tr}, wantListDb: "True", wantNoFilter: true},
		{name: "opt-out with explicit filter", cfg: OdooConfig{ListDb: &tr, DbFilter: "^%d$"}, wantListDb: "True", wantDbFilter: "^%d$"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := lines(tc.cfg.GetSerializedOdooConfig("x", conn))
			if got["list_db"] != tc.wantListDb {
				t.Errorf("list_db = %q, want %q", got["list_db"], tc.wantListDb)
			}
			filter, ok := got["dbfilter"]
			if tc.wantNoFilter && ok {
				t.Errorf("unexpected dbfilter %q", filter)
			}
			if !tc.wantNoFilter && filter != tc.wantDbFilter {
				t.Errorf("dbfilter = %q, want %q", filter, tc.wantDbFilter)
			}
		})
	}
}

func TestGetSerializedOdooConfig_DbFilterQuotesMeta(t *testing.T) {
	conn := testConn()
	conn.Name = "odoo.prod"
	got := lines((&OdooConfig{}).GetSerializedOdooConfig("x", conn))
	if got["dbfilter"] != `^odoo\.prod$` {
		t.Errorf("dbfilter = %q, want the regex-quoted name", got["dbfilter"])
	}
	if !regexp.MustCompile(got["dbfilter"]).MatchString("odoo.prod") || regexp.MustCompile(got["dbfilter"]).MatchString("odooXprod") {
		t.Errorf("dbfilter %q does not match exactly the database name", got["dbfilter"])
	}
}

func TestGetSerializedOdooConfig_ServerWideModulesAndAddons(t *testing.T) {
	cfg := &OdooConfig{
		DataDir:           "/var/lib/odoo",
		ServerWideModules: []string{"base", "web"},
		ExtraAddonsPaths:  []string{"/mnt/addons-a", "/mnt/addons-b"},
	}
	got := lines(cfg.GetSerializedOdooConfig("x", testConn()))
	if got["server_wide_modules"] != "base,web" {
		t.Errorf("server_wide_modules = %q", got["server_wide_modules"])
	}
	if got["addons_path"] != "/mnt/addons-a,/mnt/addons-b" {
		t.Errorf("addons_path = %q", got["addons_path"])
	}
}

func TestGetSerializedOdooConfig_ExtraOptionsStripNewlines(t *testing.T) {
	cfg := &OdooConfig{ExtraOptions: map[string]OdooOptionValue{"smtp_server": "mail\nlist_db = True\r"}}
	got := cfg.GetSerializedOdooConfig("x", testConn())
	if strings.Count(got, "\nlist_db = ") != 1 {
		t.Errorf("a newline in an extra option must not inject a new line:\n%s", got)
	}
	if !strings.Contains(got, "smtp_server = maillist_db = True\n") {
		t.Errorf("CR/LF should be stripped, got:\n%s", got)
	}
}

func TestGetSerializedOdooConfig_ZeroValuesSurvive(t *testing.T) {
	zero := int32(0)
	f := false
	cfg := &OdooConfig{Workers: &zero, MaxCronThreads: &zero, WithoutDemo: &f, ProxyMode: &f}
	got := lines(cfg.GetSerializedOdooConfig("x", testConn()))
	for k, v := range map[string]string{"workers": "0", "max_cron_threads": "0", "without_demo": "false", "proxy_mode": "false"} {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestInvalidExtraOptionKeys(t *testing.T) {
	cfg := &OdooConfig{ExtraOptions: map[string]OdooOptionValue{"smtp_server": "x", "Bad Key": "y", "1st": "z", "_ok": "w"}}
	got := cfg.InvalidExtraOptionKeys()
	if strings.Join(got, ",") != "1st,Bad Key" {
		t.Errorf("InvalidExtraOptionKeys() = %v", got)
	}
	if (&OdooConfig{}).InvalidExtraOptionKeys() != nil {
		t.Error("no extraOptions → nil")
	}
}

func TestConfigHash(t *testing.T) {
	a := ConfigHash("[options]\nworkers = 0\n")
	b := ConfigHash("[options]\nworkers = 1\n")
	if len(a) != 64 || a == b {
		t.Errorf("hash should be 64 hex chars and change with content: %s %s", a, b)
	}
	if a != ConfigHash("[options]\nworkers = 0\n") {
		t.Error("hash is not stable")
	}
}

func TestMaintenanceJobName(t *testing.T) {
	o := minimalOdooDeployment([]string{"base"}, []string{"base"})
	if got := o.MaintenanceJobName([]string{"base"}, true); got != "test-odoo-init" {
		t.Errorf("first boot name = %q", got)
	}
	upgrade := o.MaintenanceJobName(nil, false)
	if !regexp.MustCompile(`^test-odoo-upgrade-[0-9a-f]{8}$`).MatchString(upgrade) {
		t.Errorf("upgrade name = %q", upgrade)
	}
	if again := o.MaintenanceJobName(nil, false); again != upgrade {
		t.Errorf("upgrade name is not stable: %q vs %q", upgrade, again)
	}
	o.Spec.Image = "odoo:18.1"
	if changed := o.MaintenanceJobName(nil, false); changed == upgrade {
		t.Error("upgrade name must change with the image")
	}
	o.Spec.Image = "odoo:18"
	o.Spec.Upgrade.Token = "v2"
	if changed := o.MaintenanceJobName(nil, false); changed == upgrade {
		t.Error("upgrade name must change with the token")
	}
	o.Spec.Upgrade.Token = ""
	if changed := o.MaintenanceJobName([]string{"sale"}, false); changed == upgrade {
		t.Error("upgrade name must change with the modules to install")
	}
}

func TestGetMaintenanceJobTemplate_Command(t *testing.T) {
	tests := []struct {
		name        string
		init, up    []string
		langs       []string
		firstBoot   bool
		wantSuffix  []string
		wantAbsent  []string
		wantJobKind string
	}{
		{
			name: "first boot installs and loads languages", init: []string{"base", "web"}, langs: []string{"en_US", "ar_001"}, firstBoot: true,
			wantSuffix: []string{"--stop-after-init", "--no-http", "-i", "base,web", "--load-language=en_US,ar_001"}, wantAbsent: []string{"-u"}, wantJobKind: "init",
		},
		{
			name: "upgrade only never loads languages", up: []string{"base", "web"}, langs: []string{"en_US"},
			wantSuffix: []string{"--stop-after-init", "--no-http", "-u", "base,web"}, wantAbsent: []string{"-i", "--load-language=en_US"}, wantJobKind: "upgrade",
		},
		{
			name: "new modules and new image combine -i and -u", init: []string{"sale"}, up: []string{"base", "web"}, langs: []string{"en_US"},
			wantSuffix: []string{"--stop-after-init", "--no-http", "-i", "sale", "-u", "base,web", "--load-language=en_US"}, wantJobKind: "upgrade",
		},
		{
			name: "no languages configured", init: []string{"base"}, firstBoot: true,
			wantSuffix: []string{"--stop-after-init", "--no-http", "-i", "base"}, wantAbsent: []string{"--load-language="}, wantJobKind: "init",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := minimalOdooDeployment([]string{"base", "web"}, nil)
			o.Spec.Config.LoadLanguages = tc.langs
			job := o.GetMaintenanceJobTemplate(tc.init, tc.up, tc.firstBoot)
			cmd := job.Spec.Template.Spec.Containers[0].Command
			wantPrefix := []string{"odoo", "-c", "/opt/odoo/odoo.conf"}
			full := append(append([]string{}, wantPrefix...), tc.wantSuffix...)
			if strings.Join(cmd, " ") != strings.Join(full, " ") {
				t.Errorf("command = %v, want %v", cmd, full)
			}
			for _, absent := range tc.wantAbsent {
				for _, arg := range cmd {
					if arg == absent || strings.HasPrefix(arg, absent) && strings.HasSuffix(absent, "=") {
						t.Errorf("command %v must not contain %q", cmd, absent)
					}
				}
			}
			if job.Labels[LabelJobKind] != tc.wantJobKind || job.Spec.Template.Labels[LabelJobKind] != tc.wantJobKind {
				t.Errorf("job-kind label = %q/%q, want %q", job.Labels[LabelJobKind], job.Spec.Template.Labels[LabelJobKind], tc.wantJobKind)
			}
		})
	}
}

func TestGetMaintenanceJobTemplate_PodShape(t *testing.T) {
	o := minimalOdooDeployment([]string{"base"}, nil)
	o.Spec.Env = []corev1.EnvVar{{Name: "OTEL_SDK_DISABLED", Value: "true"}}
	o.Spec.Resources = corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")}}
	o.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "ghcr-pull"}}
	deadline, ttl, backoff := int64(120), int32(600), int32(1)
	o.Spec.Jobs = OdooJobConfig{ActiveDeadlineSeconds: &deadline, TTLSecondsAfterFinished: &ttl, BackoffLimit: &backoff}

	job := o.GetMaintenanceJobTemplate([]string{"base"}, nil, true)
	pod := job.Spec.Template.Spec
	c := pod.Containers[0]

	if _, hasApp := job.Spec.Template.Labels["app"]; hasApp {
		t.Error("job pods must not carry the app label (they would be selected by the Services)")
	}
	if job.Spec.Template.Labels[LabelOdooDeployment] != "test-odoo" {
		t.Error("job pods must carry the odoodeployment label")
	}
	if len(c.Ports) != 0 {
		t.Errorf("ports must be cleared, got %v", c.Ports)
	}
	if c.ReadinessProbe != nil || c.LivenessProbe != nil || c.StartupProbe != nil {
		t.Error("probes must be cleared on a --no-http job")
	}
	if pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %s", pod.RestartPolicy)
	}
	if len(c.Env) != 1 || c.Env[0].Name != "OTEL_SDK_DISABLED" {
		t.Errorf("env not propagated: %v", c.Env)
	}
	if !c.Resources.Requests.Cpu().Equal(resource.MustParse("250m")) {
		t.Errorf("resources not propagated: %v", c.Resources)
	}
	if len(pod.ImagePullSecrets) != 1 || pod.ImagePullSecrets[0].Name != "ghcr-pull" {
		t.Errorf("imagePullSecrets not propagated: %v", pod.ImagePullSecrets)
	}
	if *job.Spec.ActiveDeadlineSeconds != 120 || *job.Spec.TTLSecondsAfterFinished != 600 || *job.Spec.BackoffLimit != 1 {
		t.Errorf("job tunables not applied: %+v", job.Spec)
	}
	if c.Image != "odoo:18" {
		t.Errorf("job image = %q, want spec.image", c.Image)
	}
}

func TestGetMaintenanceJobTemplate_Defaults(t *testing.T) {
	o := minimalOdooDeployment([]string{"base"}, nil)
	job := o.GetMaintenanceJobTemplate([]string{"base"}, nil, true)
	if *job.Spec.ActiveDeadlineSeconds != 3600 || *job.Spec.TTLSecondsAfterFinished != 86400 || *job.Spec.BackoffLimit != 2 {
		t.Errorf("job defaults: %+v", job.Spec)
	}
}

func TestGetPodSpec_Probes(t *testing.T) {
	o := minimalOdooDeployment([]string{"base"}, nil)
	c := o.GetPodSpec().Containers[0]
	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet.Path != "/web/health?db_server_status=1" || c.ReadinessProbe.PeriodSeconds != 10 {
		t.Errorf("readiness probe = %+v", c.ReadinessProbe)
	}
	if c.LivenessProbe == nil || c.LivenessProbe.HTTPGet.Path != "/web/health" || c.LivenessProbe.InitialDelaySeconds != 60 || c.LivenessProbe.FailureThreshold != 6 {
		t.Errorf("liveness probe = %+v", c.LivenessProbe)
	}
	if c.StartupProbe == nil || c.StartupProbe.FailureThreshold != 60 || c.StartupProbe.PeriodSeconds != 10 {
		t.Errorf("startup probe = %+v", c.StartupProbe)
	}
	for _, p := range []*corev1.Probe{c.ReadinessProbe, c.LivenessProbe, c.StartupProbe} {
		if p.SuccessThreshold != 1 || p.TimeoutSeconds == 0 || p.HTTPGet.Scheme != corev1.URISchemeHTTP || p.HTTPGet.Port.IntValue() != 8069 {
			t.Errorf("probe numeric fields must all be set: %+v", p)
		}
	}

	disabled := false
	o.Spec.Probes.Enabled = &disabled
	c = o.GetPodSpec().Containers[0]
	if c.ReadinessProbe != nil || c.LivenessProbe != nil || c.StartupProbe != nil {
		t.Error("probes.enabled=false must remove all probes")
	}

	o.Spec.Probes.Enabled = nil
	o.Spec.Probes.Liveness = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstrFromInt(8069)}}}
	c = o.GetPodSpec().Containers[0]
	if c.LivenessProbe.TCPSocket == nil || c.ReadinessProbe.HTTPGet == nil {
		t.Error("an override replaces only its own probe")
	}
}

func TestGetPodSpec_SecurityContext(t *testing.T) {
	o := minimalOdooDeployment([]string{"base"}, nil)
	sc := o.GetPodSpec().SecurityContext
	if *sc.RunAsUser != 100 || *sc.RunAsGroup != 101 || *sc.FSGroup != 101 || !*sc.RunAsNonRoot {
		t.Errorf("default security context = %+v", sc)
	}
	uid := int64(101)
	o.Spec.PodSecurityContext = &corev1.PodSecurityContext{RunAsUser: &uid}
	sc = o.GetPodSpec().SecurityContext
	if *sc.RunAsUser != 101 || sc.FSGroup != nil {
		t.Errorf("custom security context must be used verbatim: %+v", sc)
	}
}

func TestGetDeploymentTemplate(t *testing.T) {
	o := minimalOdooDeployment([]string{"base"}, []string{"base"})
	d := o.GetDeploymentTemplate("ghcr.io/x/odoo:sha-abc", 0, "deadbeef")
	if *d.Spec.Replicas != 0 {
		t.Errorf("replicas = %d, want 0", *d.Spec.Replicas)
	}
	if d.Spec.Template.Spec.Containers[0].Image != "ghcr.io/x/odoo:sha-abc" {
		t.Errorf("image = %q", d.Spec.Template.Spec.Containers[0].Image)
	}
	if d.Spec.Template.Annotations[AnnotationConfigHash] != "deadbeef" {
		t.Errorf("config hash annotation = %q", d.Spec.Template.Annotations[AnnotationConfigHash])
	}
	if d.Spec.Strategy.Type != "Recreate" || d.Spec.Strategy.RollingUpdate != nil {
		t.Errorf("strategy = %+v, want Recreate", d.Spec.Strategy)
	}
	if d.Spec.Selector.MatchLabels["app"] != "test-odoo" || len(d.Spec.Selector.MatchLabels) != 1 {
		t.Errorf("selector must stay {app: name}: %v", d.Spec.Selector.MatchLabels)
	}
	if d.Spec.Template.Labels["app"] != "test-odoo" || d.Spec.Template.Labels[LabelOdooDeployment] != "test-odoo" {
		t.Errorf("pod labels = %v", d.Spec.Template.Labels)
	}
}

func TestDeduplicateModules(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{name: "no duplicates unchanged", input: []string{"base", "web", "sale"}, want: []string{"base", "web", "sale"}},
		{name: "duplicates removed preserving order", input: []string{"base", "web", "base", "sale", "web"}, want: []string{"base", "web", "sale"}},
		{name: "all duplicates reduced to one", input: []string{"base", "base", "base"}, want: []string{"base"}},
		{name: "empty slice unchanged", input: []string{}, want: []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := minimalOdooDeployment(tc.input, []string{})
			o.Spec.Upgrade.Modules = append([]string{}, tc.input...)
			o.DeduplicateModules()
			if strings.Join(o.Spec.Modules, ",") != strings.Join(tc.want, ",") {
				t.Errorf("DeduplicateModules() = %v, want %v", o.Spec.Modules, tc.want)
			}
			if strings.Join(o.Spec.Upgrade.Modules, ",") != strings.Join(tc.want, ",") {
				t.Errorf("upgrade modules = %v, want %v", o.Spec.Upgrade.Modules, tc.want)
			}
		})
	}
}

func TestGetPodSpec_OdooCommand(t *testing.T) {
	tests := []struct {
		name        string
		odooCommand []string
		wantPrefix  []string
	}{
		{name: "default single binary", odooCommand: []string{"odoo"}, wantPrefix: []string{"odoo", "-c", "/opt/odoo/odoo.conf"}},
		{name: "entrypoint script", odooCommand: []string{"/entrypoint.sh"}, wantPrefix: []string{"/entrypoint.sh", "-c", "/opt/odoo/odoo.conf"}},
		{name: "command with pre-arguments", odooCommand: []string{"/usr/bin/env", "odoo"}, wantPrefix: []string{"/usr/bin/env", "odoo", "-c", "/opt/odoo/odoo.conf"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := minimalOdooDeployment([]string{"base"}, []string{})
			o.Spec.OdooCommand = tc.odooCommand
			cmd := o.GetPodSpec().Containers[0].Command
			if strings.Join(cmd, " ") != strings.Join(tc.wantPrefix, " ") {
				t.Errorf("Command = %v, want %v", cmd, tc.wantPrefix)
			}
			job := o.GetMaintenanceJobTemplate([]string{"base"}, nil, true)
			jobCmd := job.Spec.Template.Spec.Containers[0].Command
			if strings.Join(jobCmd[:len(tc.wantPrefix)], " ") != strings.Join(tc.wantPrefix, " ") {
				t.Errorf("job Command = %v, want prefix %v", jobCmd, tc.wantPrefix)
			}
		})
	}
}

func TestPointerAccessors(t *testing.T) {
	spec := OdooDeploymentSpec{}
	if spec.ReplicasValue() != 1 || spec.Config.WorkersValue() != 2 || spec.Config.MaxCronThreadsValue() != 1 ||
		!spec.Config.WithoutDemoValue() || !spec.Config.ProxyModeValue() || spec.Config.ListDbValue() ||
		!spec.Upgrade.OnImageChangeValue() || !spec.Probes.EnabledValue() ||
		spec.Database.CreatePolicyValue() != DatabaseCreatePolicyIfNotExists ||
		spec.Database.DeletionPolicyValue() != DeletionPolicyRetain ||
		spec.Database.MaintenanceDatabaseValue() != "postgres" ||
		spec.OdooFilestore.DeletionPolicyValue() != DeletionPolicyRetain {
		t.Errorf("nil accessors must return the CRD defaults: %+v", spec)
	}
	zero, f := int32(0), false
	spec.Replicas, spec.Config.Workers, spec.Upgrade.OnImageChange = &zero, &zero, &f
	if spec.ReplicasValue() != 0 || spec.Config.WorkersValue() != 0 || spec.Upgrade.OnImageChangeValue() {
		t.Error("explicit zero values must be honoured")
	}
}

func intstrFromInt(i int32) intstr.IntOrString { return intstr.FromInt32(i) }
