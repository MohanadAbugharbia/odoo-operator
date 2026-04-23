package v1

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// minimalOdooDeployment returns an OdooDeployment with just enough fields
// set to call GetDbInitJobTemplate without panicking.
func minimalOdooDeployment(specModules, installedModules []string) *OdooDeployment {
	return &OdooDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-odoo",
			Namespace: "default",
		},
		Spec: OdooDeploymentSpec{
			Image: "odoo:18",
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
