package v1

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// minimalOdooDeployment returns an OdooDeployment with just enough fields
// set to call GetDbInitJobTemplate without panicking.
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

func TestGetDbInitJobTemplate_FreshInstall(t *testing.T) {
	o := minimalOdooDeployment(
		[]string{"base", "web", "sale"},
		[]string{},
	)

	job, modules := o.GetDbInitJobTemplate()

	if len(modules) != 3 {
		t.Fatalf("expected 3 modules, got %d: %v", len(modules), modules)
	}
	cmd := job.Spec.Template.Spec.Containers[0].Command
	initFlag := cmd[len(cmd)-1]
	if !strings.Contains(initFlag, "base") || !strings.Contains(initFlag, "web") || !strings.Contains(initFlag, "sale") {
		t.Errorf("--init flag %q missing expected modules", initFlag)
	}
}

func TestGetDbInitJobTemplate_PartialInstall(t *testing.T) {
	o := minimalOdooDeployment(
		[]string{"base", "web", "sale"},
		[]string{"base", "web"},
	)

	job, modules := o.GetDbInitJobTemplate()

	if len(modules) != 1 || modules[0] != "sale" {
		t.Fatalf("expected [sale], got %v", modules)
	}
	cmd := job.Spec.Template.Spec.Containers[0].Command
	initFlag := cmd[len(cmd)-1]
	if initFlag != "sale" {
		t.Errorf("--init flag = %q, want %q", initFlag, "sale")
	}
}

func TestGetDbInitJobTemplate_AllInstalled(t *testing.T) {
	o := minimalOdooDeployment(
		[]string{"base", "web"},
		[]string{"base", "web"},
	)

	job, modules := o.GetDbInitJobTemplate()

	if len(modules) != 0 {
		t.Fatalf("expected empty modulesToInstall, got %v", modules)
	}
	// Job should be zero-value — no panic
	if job.Name != "" {
		t.Errorf("expected empty Job, got Name=%q", job.Name)
	}
}

func TestGetSerializedOdooConfig_ExtraAddonsPaths(t *testing.T) {
	baseConfig := &OdooConfig{
		DataDir:         "/var/lib/odoo",
		Workers:         2,
		LimitMemorySoft: 2147483648,
		LimitMemoryHard: 2684354560,
	}

	tests := []struct {
		name             string
		extraAddonsPaths []string
		wantContains     string
		wantAbsent       string
	}{
		{
			name:             "no paths omits addons_path line",
			extraAddonsPaths: []string{},
			wantAbsent:       "addons_path",
		},
		{
			name:             "single path produces correct addons_path line",
			extraAddonsPaths: []string{"/mnt/extra-addons"},
			wantContains:     "addons_path = /mnt/extra-addons\n",
		},
		{
			name:             "multiple paths are comma-joined with no trailing comma",
			extraAddonsPaths: []string{"/mnt/addons-a", "/mnt/addons-b"},
			wantContains:     "addons_path = /mnt/addons-a,/mnt/addons-b\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := baseConfig.GetSerializedOdooConfig(
				"adminpass", "localhost", 5432, "odoo", "dbpass", 20, "odoo",
				tc.extraAddonsPaths,
			)
			if tc.wantContains != "" && !strings.Contains(got, tc.wantContains) {
				t.Errorf("config missing %q\ngot:\n%s", tc.wantContains, got)
			}
			if tc.wantAbsent != "" && strings.Contains(got, tc.wantAbsent) {
				t.Errorf("config unexpectedly contains %q\ngot:\n%s", tc.wantAbsent, got)
			}
		})
	}
}

func TestDeduplicateModules(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "no duplicates unchanged",
			input: []string{"base", "web", "sale"},
			want:  []string{"base", "web", "sale"},
		},
		{
			name:  "duplicates removed preserving order",
			input: []string{"base", "web", "base", "sale", "web"},
			want:  []string{"base", "web", "sale"},
		},
		{
			name:  "all duplicates reduced to one",
			input: []string{"base", "base", "base"},
			want:  []string{"base"},
		},
		{
			name:  "empty slice unchanged",
			input: []string{},
			want:  []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := minimalOdooDeployment(tc.input, []string{})
			o.DeduplicateModules()
			if len(o.Spec.Modules) != len(tc.want) {
				t.Fatalf("DeduplicateModules() = %v, want %v", o.Spec.Modules, tc.want)
			}
			for i := range o.Spec.Modules {
				if o.Spec.Modules[i] != tc.want[i] {
					t.Errorf("Modules[%d] = %q, want %q", i, o.Spec.Modules[i], tc.want[i])
				}
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
		{
			name:        "default single binary",
			odooCommand: []string{"odoo"},
			wantPrefix:  []string{"odoo", "-c", "/opt/odoo/odoo.conf"},
		},
		{
			name:        "entrypoint script",
			odooCommand: []string{"/entrypoint.sh"},
			wantPrefix:  []string{"/entrypoint.sh", "-c", "/opt/odoo/odoo.conf"},
		},
		{
			name:        "command with pre-arguments",
			odooCommand: []string{"/usr/bin/env", "odoo"},
			wantPrefix:  []string{"/usr/bin/env", "odoo", "-c", "/opt/odoo/odoo.conf"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := minimalOdooDeployment([]string{"base"}, []string{})
			o.Spec.OdooCommand = tc.odooCommand

			cmd := o.GetPodSpec().Containers[0].Command

			if len(cmd) < len(tc.wantPrefix) {
				t.Fatalf("Command too short: %v", cmd)
			}
			for i, want := range tc.wantPrefix {
				if cmd[i] != want {
					t.Errorf("Command[%d] = %q, want %q (full: %v)", i, cmd[i], want, cmd)
				}
			}
		})
	}
}

func TestGetDbInitJobTemplate_OdooCommand(t *testing.T) {
	tests := []struct {
		name        string
		odooCommand []string
		wantPrefix  []string
	}{
		{
			name:        "default single binary",
			odooCommand: []string{"odoo"},
			wantPrefix:  []string{"odoo", "-c", "/opt/odoo/odoo.conf"},
		},
		{
			name:        "entrypoint script",
			odooCommand: []string{"/entrypoint.sh"},
			wantPrefix:  []string{"/entrypoint.sh", "-c", "/opt/odoo/odoo.conf"},
		},
		{
			name:        "command with pre-arguments",
			odooCommand: []string{"/usr/bin/env", "odoo"},
			wantPrefix:  []string{"/usr/bin/env", "odoo", "-c", "/opt/odoo/odoo.conf"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := minimalOdooDeployment([]string{"base"}, []string{})
			o.Spec.OdooCommand = tc.odooCommand

			job, _ := o.GetDbInitJobTemplate()
			cmd := job.Spec.Template.Spec.Containers[0].Command

			if len(cmd) < len(tc.wantPrefix) {
				t.Fatalf("Command too short: %v", cmd)
			}
			for i, want := range tc.wantPrefix {
				if cmd[i] != want {
					t.Errorf("Command[%d] = %q, want %q (full: %v)", i, cmd[i], want, cmd)
				}
			}
		})
	}
}
