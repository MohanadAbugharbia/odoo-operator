package utils

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	secretNamespace = "utils-ns"
	secretName      = "db-cred"
)

// clientWith returns a client holding one Secret with the given keys.
func clientWith(t *testing.T, data map[string][]byte) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: secretNamespace},
		Data:       data,
	}).Build()
}

// A value pasted with `echo | base64` carries a trailing newline; feeding that
// into a DSN or a port number is the bug these helpers exist to prevent.
func TestGetSecretValueTrimsWhitespace(t *testing.T) {
	c := clientWith(t, map[string][]byte{
		"password": []byte("  s3cret\n"),
		"empty":    []byte("   "),
	})

	got, err := GetSecretValue(c, context.Background(), secretNamespace, secretName, "password")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3cret" {
		t.Errorf("value = %q, want %q", got, "s3cret")
	}

	got, err = GetSecretValue(c, context.Background(), secretNamespace, secretName, "empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("all-whitespace value = %q, want empty", got)
	}
}

func TestGetSecretValueMissing(t *testing.T) {
	c := clientWith(t, map[string][]byte{"password": []byte("x")})

	_, err := GetSecretValue(c, context.Background(), secretNamespace, "no-such-secret", "password")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("missing Secret: err = %v, want ErrSecretNotFound", err)
	}

	_, err = GetSecretValue(c, context.Background(), secretNamespace, secretName, "no-such-key")
	if !errors.Is(err, ErrSecretKeyNotFound) {
		t.Errorf("missing key: err = %v, want ErrSecretKeyNotFound", err)
	}

	_, err = GetSecretValue(c, context.Background(), "other-namespace", secretName, "password")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("wrong namespace: err = %v, want ErrSecretNotFound", err)
	}
}

func TestGetInt32SecretValue(t *testing.T) {
	c := clientWith(t, map[string][]byte{
		"port":     []byte("5432\n"),
		"zero":     []byte("0"),
		"negative": []byte("-1"),
		"overflow": []byte("2147483648"), // math.MaxInt32 + 1
		"words":    []byte("five thousand"),
	})
	ctx := context.Background()

	tests := []struct {
		key     string
		want    int32
		wantErr bool
	}{
		{key: "port", want: 5432},
		{key: "zero", want: 0},
		{key: "negative", want: -1},
		{key: "overflow", wantErr: true},
		{key: "words", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			got, err := GetInt32SecretValue(c, ctx, secretNamespace, secretName, tc.key)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("value = %d, want %d", got, tc.want)
			}
		})
	}

	_, err := GetInt32SecretValue(c, ctx, secretNamespace, secretName, "absent")
	if !errors.Is(err, ErrSecretKeyNotFound) {
		t.Errorf("missing key: err = %v, want ErrSecretKeyNotFound", err)
	}
}

func TestGetBoolSecretValue(t *testing.T) {
	c := clientWith(t, map[string][]byte{
		"true":    []byte("true\n"),
		"one":     []byte("1"),
		"upper":   []byte("TRUE"),
		"false":   []byte("false"),
		"zero":    []byte("0"),
		"garbage": []byte("yes please"),
	})
	ctx := context.Background()

	tests := []struct {
		key     string
		want    bool
		wantErr bool
	}{
		{key: "true", want: true},
		{key: "one", want: true},
		{key: "upper", want: true},
		{key: "false", want: false},
		{key: "zero", want: false},
		{key: "garbage", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			got, err := GetBoolSecretValue(c, ctx, secretNamespace, secretName, tc.key)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("value = %v, want %v", got, tc.want)
			}
		})
	}

	if _, err := GetBoolSecretValue(c, ctx, secretNamespace, "gone", "true"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("missing Secret: err = %v, want ErrSecretNotFound", err)
	}
}
