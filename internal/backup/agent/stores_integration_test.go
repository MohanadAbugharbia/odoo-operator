/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package agent

// Store tests: the Job configuration jobspec.Locate builds for each store
// kind drives the real restic and rclone against real servers. The
// Kubernetes parts are emulated: secret env vars are resolved from a map and
// the Job's mount paths are rewritten to temporary folders.
//
// SFTP runs with TEST_SFTP_HOST, TEST_SFTP_PORT, TEST_SFTP_USER,
// TEST_SFTP_KEY_FILE (an ed25519 private key), TEST_SFTP_KNOWN_HOSTS (a
// known_hosts file for the server) and TEST_SFTP_PATH (a writable folder).
// S3 runs with TEST_S3_ENDPOINT, TEST_S3_BUCKET, TEST_S3_ACCESS_KEY_ID and
// TEST_S3_SECRET_ACCESS_KEY. rclone must be on PATH.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/jobspec"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/retention"
)

// secrets are the emulated Secrets: name → key → value.
type secrets map[string]map[string]string

// jobEnvironment is what a Job built from a store sees.
type jobEnvironment struct {
	env         []string
	options     []string
	probeTarget string
}

// emulateJob resolves the store's Job configuration on this machine.
func emulateJob(t *testing.T, store *odoov1.OdooBackupStore, sec secrets, repo string) jobEnvironment {
	t.Helper()
	loc, err := jobspec.Locate(store, repo)
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	return emulateLocation(t, loc, sec, filepath.Join(t.TempDir(), "store"))
}

// storeMountRE is where a Job mounts an nfs or pvc store, its own or a copy's.
var storeMountRE = regexp.MustCompile(regexp.QuoteMeta(layout.StoreMount) + `(-copy[0-9]+)?`)

// emulateLocation resolves one store's part of a Job on this machine, with
// an nfs or pvc store's mount at storeDir.
func emulateLocation(t *testing.T, loc jobspec.Location, sec secrets, storeDir string) jobEnvironment {
	t.Helper()
	secretsDir := t.TempDir()
	rewrite := func(s string) string {
		s = strings.ReplaceAll(s, layout.SecretsMount, secretsDir)
		return storeMountRE.ReplaceAllString(s, storeDir)
	}
	volumes := map[string]corev1.Volume{}
	for _, v := range loc.Volumes {
		volumes[v.Name] = v
	}
	for _, m := range loc.Mounts {
		v := volumes[m.Name]
		dir := rewrite(m.MountPath)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if v.Secret == nil {
			continue
		}
		for _, item := range v.Secret.Items {
			value, ok := sec[v.Secret.SecretName][item.Key]
			if !ok && v.Secret.Optional != nil && *v.Secret.Optional {
				continue // the kubelet leaves a missing key out
			}
			if !ok {
				t.Fatalf("the Job mounts key %s of Secret %s, which the test does not have", item.Key, v.Secret.SecretName)
			}
			if err := os.WriteFile(filepath.Join(dir, item.Path), []byte(value), 0o400); err != nil {
				t.Fatal(err)
			}
		}
	}
	je := jobEnvironment{options: loc.ResticOptions, probeTarget: rewrite(loc.ProbeTarget)}
	for _, e := range loc.Env {
		value := e.Value
		if e.ValueFrom != nil {
			ref := e.ValueFrom.SecretKeyRef
			if ref == nil {
				t.Fatalf("env %s comes from something other than a Secret", e.Name)
			}
			v, ok := sec[ref.Name][ref.Key]
			if !ok {
				t.Fatalf("env %s reads key %s of Secret %s, which the test does not have", e.Name, ref.Key, ref.Name)
			}
			value = v
		}
		je.env = append(je.env, e.Name+"="+rewrite(value))
	}
	return je
}

// TestCopies copies backups between every two kinds of store, each with its
// own key (and its own credentials), applies retention in the copy, and
// restores from the copy as from a store of its own.
func TestCopies(t *testing.T) {
	cases := storeCases(t)
	for i := range cases {
		from, to := cases[i], cases[(i+1)%len(cases)]
		t.Run(from.name+"-to-"+to.name, func(t *testing.T) {
			f := newFixture(t)
			f.seed(8)
			repo := layout.Repository(itNamespace, itDeployment)
			primary := emulateJob(t, from.store, from.secrets, repo)
			copyStore := to.store.DeepCopy()
			copyStore.Name = "copy-store"
			copyStore.Spec.EncryptionKeySecret.Name = "copy-key"
			// Its own folder: the same store is another case's own store.
			switch sp := &copyStore.Spec; {
			case sp.S3 != nil:
				sp.S3.Path += "-copy"
			case sp.SFTP != nil:
				sp.SFTP.Path += "-copy"
			}
			copySecrets := copySecrets(to.secrets)
			copySecrets["copy-key"] = map[string]string{"password": "another key " + randomHex(t, 8)}
			loc, err := jobspec.LocateCopy(copyStore, repo, 1)
			if err != nil {
				t.Fatal(err)
			}
			copyDir := filepath.Join(t.TempDir(), "copy-store")
			second := emulateLocation(t, loc, copySecrets, copyDir)
			f.env.Restic.Env = f.env.Restic.Env[2:]
			f.env.Restic.Options = primary.options
			setEnv(t, append(primary.env, second.env...))
			targets, err := CopyTargetsFromEnv(f.env.Restic, []string{copyStore.Name})
			if err != nil {
				t.Fatal(err)
			}
			ret := &Retention{Policy: retention.Policy{Last: 1}}
			withCopies := func(o *BackupOptions) { o.Kind = retention.KindScheduled; o.Copies = targets; o.Retention = ret }

			res1 := f.mustBackup("one", withCopies)
			if len(res1.Copies) != 1 || res1.Copies[0].Error != "" || res1.Copies[0].SnapshotID == "" {
				t.Fatalf("copy: %+v\n%s", res1.Copies, f.logs.String())
			}
			before := f.state(f.db)
			res2 := f.mustBackup("two", withCopies)
			c := res2.Copies[0]
			if c.Error != "" || c.SnapshotID == "" || c.SnapshotID == res1.Copies[0].SnapshotID {
				t.Fatalf("second copy: %+v", res2.Copies)
			}
			host := layout.Host(itNamespace, itDeployment)
			left, err := targets[0].Restic.Snapshots(context.Background(), host)
			if err != nil {
				t.Fatal(err)
			}
			if len(left) != 1 || left[0].ID != c.SnapshotID {
				t.Fatalf("retention in the copy left %d snapshot(s), want only the second", len(left))
			}

			By := t.Log
			By("the copy restores on its own, through the store's normal configuration")
			f.mutate()
			ownLoc, err := jobspec.Locate(copyStore, repo)
			if err != nil {
				t.Fatal(err)
			}
			own := emulateLocation(t, ownLoc, copySecrets, copyDir)
			f.env.Restic.Options = own.options
			setEnv(t, own.env)
			if rr := f.restore(c.SnapshotID, f.nextSuffix()); !rr.OK {
				t.Fatalf("restore from the copy: %+v", rr)
			}
			f.assertState(f.db, before, "restored from the copy in "+to.name)
		})
	}
}

func storeObject(spec odoov1.OdooBackupStoreSpec) *odoov1.OdooBackupStore {
	spec.EncryptionKeySecret = corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "restic-key"}, Key: "password",
	}
	return &odoov1.OdooBackupStore{
		ObjectMeta: metav1.ObjectMeta{Name: "store", Namespace: itNamespace},
		Spec:       spec,
	}
}

type storeCase struct {
	name    string
	store   *odoov1.OdooBackupStore
	secrets secrets
	// broken returns secrets the probe must reject.
	broken func(secrets) secrets
}

func copySecrets(s secrets) secrets {
	out := secrets{}
	for n, kv := range s {
		out[n] = map[string]string{}
		for k, v := range kv {
			out[n][k] = v
		}
	}
	return out
}

func storeCases(t *testing.T) []storeCase {
	t.Helper()
	key := secrets{"restic-key": {"password": "correct horse battery staple"}}
	cases := []storeCase{
		{
			name:    "pvc",
			store:   storeObject(odoov1.OdooBackupStoreSpec{PVC: &odoov1.PVCStore{ClaimName: "backups", SubPath: "odoo/backups"}}),
			secrets: key,
		},
		{
			name:    "nfs",
			store:   storeObject(odoov1.OdooBackupStoreSpec{NFS: &odoov1.NFSStore{Server: "nfs.example.com", Path: "/exports/odoo"}}),
			secrets: key,
		},
	}
	if host := os.Getenv("TEST_SFTP_HOST"); host != "" {
		keyData, err := os.ReadFile(os.Getenv("TEST_SFTP_KEY_FILE"))
		if err != nil {
			t.Fatalf("TEST_SFTP_KEY_FILE: %v", err)
		}
		knownHosts, err := os.ReadFile(os.Getenv("TEST_SFTP_KNOWN_HOSTS"))
		if err != nil {
			t.Fatalf("TEST_SFTP_KNOWN_HOSTS: %v", err)
		}
		port := int32(22)
		if p := os.Getenv("TEST_SFTP_PORT"); p != "" {
			n, err := parseInt32(p)
			if err != nil {
				t.Fatalf("TEST_SFTP_PORT: %v", err)
			}
			port = n
		}
		sftpCase := func(name, secretKey string, keyData []byte) storeCase {
			sec := copySecrets(key)
			sec["sftp"] = map[string]string{secretKey: string(keyData), odoov1.SSHKnownHostsKey: string(knownHosts)}
			return storeCase{
				name: name,
				store: storeObject(odoov1.OdooBackupStoreSpec{SFTP: &odoov1.SFTPStore{
					Host: host, Port: port, User: os.Getenv("TEST_SFTP_USER"),
					Path:      odoov1.StorePath(os.Getenv("TEST_SFTP_PATH") + "/" + randomHex(t, 4)),
					SSHSecret: corev1.LocalObjectReference{Name: "sftp"},
				}}),
				secrets: sec,
				broken: func(s secrets) secrets {
					// A server whose host key does not match is refused.
					s = copySecrets(s)
					s["sftp"][odoov1.SSHKnownHostsKey] = "[" + host + "]:1 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIN3iAlYu0OFlbFtu1hWW2fb+qv/lDpobr1GQY/3t7dJI\n" +
						strings.Replace(string(knownHosts), "AAAA", "AAAB", 1)
					return s
				},
			}
		}
		cases = append(cases, sftpCase("sftp", odoov1.SSHPrivateKeyKey, keyData))
		// Other client key types, also under the ssh-auth Secret's key name.
		for _, k := range []struct{ name, env, secretKey string }{
			{"sftp-rsa", "TEST_SFTP_RSA_KEY_FILE", odoov1.SSHAuthPrivateKeyKey},
			{"sftp-ecdsa", "TEST_SFTP_ECDSA_KEY_FILE", odoov1.SSHPrivateKeyKey},
		} {
			if os.Getenv(k.env) == "" {
				t.Logf("%s is not set, skipping %s", k.env, k.name)
				continue
			}
			data, err := os.ReadFile(os.Getenv(k.env))
			if err != nil {
				t.Fatalf("%s: %v", k.env, err)
			}
			cases = append(cases, sftpCase(k.name, k.secretKey, data))
		}
	} else {
		t.Log("TEST_SFTP_HOST is not set, skipping the SFTP store")
	}
	if endpoint := os.Getenv("TEST_S3_ENDPOINT"); endpoint != "" {
		sec := copySecrets(key)
		sec["s3"] = map[string]string{
			odoov1.S3AccessKeyIDKey:     os.Getenv("TEST_S3_ACCESS_KEY_ID"),
			odoov1.S3SecretAccessKeyKey: os.Getenv("TEST_S3_SECRET_ACCESS_KEY"),
		}
		cases = append(cases, storeCase{
			name: "s3",
			store: storeObject(odoov1.OdooBackupStoreSpec{S3: &odoov1.S3Store{
				Endpoint: endpoint, Region: "us-east-1", Bucket: os.Getenv("TEST_S3_BUCKET"),
				Path: odoov1.StorePath("it/" + randomHex(t, 4)), ForcePathStyle: true,
				CredentialsSecret: corev1.LocalObjectReference{Name: "s3"},
			}}),
			secrets: sec,
			broken: func(s secrets) secrets {
				s = copySecrets(s)
				s["s3"][odoov1.S3SecretAccessKeyKey] = "wrong-secret"
				return s
			},
		})
	} else {
		t.Log("TEST_S3_ENDPOINT is not set, skipping the S3 store")
	}
	return cases
}

func parseInt32(s string) (int32, error) {
	var n int32
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, os.ErrInvalid
		}
		n = n*10 + (c - '0')
	}
	return n, nil
}

// setEnv sets a Job's environment for the rest of the test, the way
// odoo-backup starts: with the host key algorithms limited to known_hosts
// and the private key found under either name.
func setEnv(t *testing.T, env []string) {
	t.Helper()
	for _, name := range []string{envHostKeyAlgorithms, "RCLONE_CONFIG_COPY1_HOST_KEY_ALGORITHMS"} {
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	if err := LimitHostKeyAlgorithms(); err != nil {
		t.Fatal(err)
	}
	if err := UseSSHAuthPrivateKey(); err != nil {
		t.Fatal(err)
	}
}

// TestStores backs up, restores and probes through every store kind with
// the configuration the operator gives its Jobs.
func TestStores(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil && os.Getenv("TEST_BACKUP_PG_HOST") != "" {
		t.Fatal("rclone is not on PATH")
	}
	for _, tc := range storeCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.seed(12)
			job := emulateJob(t, tc.store, tc.secrets, layout.Repository(itNamespace, itDeployment))
			// As in the Job: the store's environment is the process's, and
			// odoo-backup limits the SSH host key algorithms first.
			f.env.Restic.Env = f.env.Restic.Env[2:] // keep the cache dir only
			f.env.Restic.Options = job.options
			setEnv(t, job.env)

			before := f.state(f.db)
			res := f.mustBackup("first")
			res2 := f.mustBackup("second")
			if res2.AddedBytes >= res.AddedBytes {
				t.Fatalf("second backup not deduplicated: %d then %d", res.AddedBytes, res2.AddedBytes)
			}
			f.mutate()
			if rr := f.restore(res.SnapshotID, f.nextSuffix()); !rr.OK {
				t.Fatalf("restore through %s: %+v", tc.name, rr)
			}
			f.assertState(f.db, before, "restored through "+tc.name)

			// The probe, with the probe Job's configuration (no repository).
			probe := emulateJob(t, tc.store, tc.secrets, "")
			setEnv(t, probe.env)
			if tc.name == "pvc" || tc.name == "nfs" {
				if err := os.MkdirAll(probe.probeTarget, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if pr := Probe(context.Background(), ProbeOptions{Target: probe.probeTarget}); !pr.OK {
				t.Fatalf("probe of a working %s store: %+v", tc.name, pr)
			}
			if tc.broken != nil {
				bad := emulateJob(t, tc.store, tc.broken(tc.secrets), "")
				setEnv(t, bad.env)
				if pr := Probe(context.Background(), ProbeOptions{Target: bad.probeTarget}); pr.OK || pr.Step != "probe" {
					t.Fatalf("probe of a %s store with broken credentials succeeded: %+v", tc.name, pr)
				}
			}
		})
	}
}

// A store folder that cannot be written fails the probe.
func TestProbeReadOnlyFolder(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone is not on PATH")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only folder")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if pr := Probe(context.Background(), ProbeOptions{Target: dir}); pr.OK {
		t.Fatalf("probe of a read-only folder succeeded")
	}
	if pr := Probe(context.Background(), ProbeOptions{Target: filepath.Join(t.TempDir(), "absent", "deeper")}); !pr.OK {
		t.Fatalf("rclone creates missing folders in a writable one: %+v", pr)
	}
}
