package v1

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Every connection field can be given inline or pulled from a Secret. The
// indirection is what a preview environment actually uses (the credentials are
// sealed, the values are not in git), so both branches are covered here.

const connNamespace = "conn-ns"

func connClient(t *testing.T, data map[string][]byte) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "conn", Namespace: connNamespace},
		Data:       data,
	}).Build()
}

func selector(key string) corev1.SecretKeySelector {
	return corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "conn"},
		Key:                  key,
	}
}

func TestGetDbConnectionDetailsInline(t *testing.T) {
	cfg := &OdooDatabaseConfig{
		// The inline values are deliberately padded: they reach the operator
		// from a CR a human typed.
		Host: " db-host ", Port: 5432, User: " odoo ", Name: " odoo_db ",
		SSL: true, MaxConn: 32,
		PasswordFromSecret: selector("password"),
	}
	c := connClient(t, map[string][]byte{"password": []byte("p@ss\n")})

	got, err := cfg.GetDbConnectionDetails(c, context.Background(), connNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := DatabaseConnectionDetails{
		Host: "db-host", Port: 5432, User: "odoo", Password: "p@ss",
		Name: "odoo_db", SSL: true, MaxConn: 32,
	}
	if got != want {
		t.Errorf("details = %+v, want %+v", got, want)
	}
}

func TestGetDbConnectionDetailsFromSecret(t *testing.T) {
	cfg := &OdooDatabaseConfig{
		HostFromSecret:     selector("host"),
		PortFromSecret:     selector("port"),
		UserFromSecret:     selector("user"),
		PasswordFromSecret: selector("password"),
		NameFromSecret:     selector("dbname"),
		SSLFromSecret:      selector("ssl"),
		MaxConnFromSecret:  selector("maxconn"),
		// The inline values must lose to the Secret ones.
		Host: "ignored", Port: 1, User: "ignored", Name: "ignored", MaxConn: 1,
	}
	c := connClient(t, map[string][]byte{
		"host":     []byte("shared-pg-rw.shared.svc.cluster.local\n"),
		"port":     []byte("5433\n"),
		"user":     []byte("pal_odoo\n"),
		"password": []byte("p@ss/w:rd\n"),
		"dbname":   []byte("pal_odoo_pr_12\n"),
		"ssl":      []byte("true\n"),
		"maxconn":  []byte("64\n"),
	})

	got, err := cfg.GetDbConnectionDetails(c, context.Background(), connNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := DatabaseConnectionDetails{
		Host:     "shared-pg-rw.shared.svc.cluster.local",
		Port:     5433,
		User:     "pal_odoo",
		Password: "p@ss/w:rd",
		Name:     "pal_odoo_pr_12",
		SSL:      true,
		MaxConn:  64,
	}
	if got != want {
		t.Errorf("details = %+v, want %+v", got, want)
	}
}

// A selector with a name but no key (or the other way round) is ignored rather
// than treated as a lookup, so the inline value still applies.
func TestGetDbConnectionDetailsIgnoresHalfSelectors(t *testing.T) {
	cfg := &OdooDatabaseConfig{
		Host: "inline-host", Port: 5432, User: "odoo", Name: "odoo",
		HostFromSecret: corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "conn"},
		},
		PortFromSecret:     corev1.SecretKeySelector{Key: "port"},
		PasswordFromSecret: selector("password"),
	}
	c := connClient(t, map[string][]byte{"password": []byte("x"), "port": []byte("9999")})

	got, err := cfg.GetDbConnectionDetails(c, context.Background(), connNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Host != "inline-host" || got.Port != 5432 {
		t.Errorf("half-specified selectors were followed: %+v", got)
	}
}

// Each failure is reported against the field that could not be resolved, so an
// operator reading the Degraded condition knows which Secret to look at.
func TestGetDbConnectionDetailsErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*OdooDatabaseConfig)
		wantMsg string
	}{
		{
			name:    "no password source at all",
			mutate:  func(cfg *OdooDatabaseConfig) { cfg.PasswordFromSecret = corev1.SecretKeySelector{} },
			wantMsg: "password",
		},
		{
			name:    "host key missing from the Secret",
			mutate:  func(cfg *OdooDatabaseConfig) { cfg.HostFromSecret = selector("absent") },
			wantMsg: "host",
		},
		{
			name:    "port is not a number",
			mutate:  func(cfg *OdooDatabaseConfig) { cfg.PortFromSecret = selector("not-a-port") },
			wantMsg: "port",
		},
		{
			name:    "ssl is not a boolean",
			mutate:  func(cfg *OdooDatabaseConfig) { cfg.SSLFromSecret = selector("not-a-bool") },
			wantMsg: "ssl",
		},
		{
			name:    "max connections is not a number",
			mutate:  func(cfg *OdooDatabaseConfig) { cfg.MaxConnFromSecret = selector("not-a-port") },
			wantMsg: "max",
		},
		{
			name:    "the whole Secret is missing",
			mutate:  func(cfg *OdooDatabaseConfig) { cfg.UserFromSecret.Name = "no-such-secret" },
			wantMsg: "user",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &OdooDatabaseConfig{
				Host: "db", Port: 5432, User: "odoo", Name: "odoo",
				PasswordFromSecret: selector("password"),
				UserFromSecret:     selector("user"),
			}
			tc.mutate(cfg)
			c := connClient(t, map[string][]byte{
				"password":   []byte("p"),
				"user":       []byte("odoo"),
				"not-a-port": []byte("http"),
				"not-a-bool": []byte("maybe"),
			})

			_, err := cfg.GetDbConnectionDetails(c, context.Background(), connNamespace)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantMsg) {
				t.Errorf("error %q does not name the failing field %q", err, tc.wantMsg)
			}
		})
	}
}
