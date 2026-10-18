package controller

import "testing"

func TestBackupImageFor(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/x/odoo-operator:1.2.3":     "ghcr.io/x/odoo-operator-backup:1.2.3",
		"ghcr.io/x/odoo-operator:main":      "ghcr.io/x/odoo-operator-backup:main",
		"odoo-operator:dev":                 "odoo-operator-backup:dev",
		"localhost:5000/op:dev":             "localhost:5000/op-backup:dev",
		"localhost:5000/op":                 "localhost:5000/op-backup",
		"localhost:5000/team/op:v1.0.0-rc1": "localhost:5000/team/op-backup:v1.0.0-rc1",
		"ghcr.io/x/odoo-operator":           "ghcr.io/x/odoo-operator-backup",
		"odoo-operator":                     "odoo-operator-backup",
		// A digest pins the operator image only: the backup image cannot
		// share it, so the tag is kept, or latest is used.
		"ghcr.io/x/odoo-operator@sha256:0123abcd":       "ghcr.io/x/odoo-operator-backup:latest",
		"localhost:5000/op@sha256:0123abcd":             "localhost:5000/op-backup:latest",
		"ghcr.io/x/odoo-operator:1.2.3@sha256:0123abcd": "ghcr.io/x/odoo-operator-backup:1.2.3",
		"localhost:5000/op:dev@sha256:0123abcd":         "localhost:5000/op-backup:dev",
	}
	for in, want := range cases {
		if got := BackupImageFor(in); got != want {
			t.Errorf("BackupImageFor(%q) = %q, want %q", in, got, want)
		}
	}
}
