package jobspec

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

const (
	ns       = "shop"
	instName = "odoo"
	image    = "ghcr.io/x/odoo-operator-backup:1.2.3"
)

func passwordRef(key string) corev1.SecretKeySelector {
	return corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "restic-pw"}, Key: key}
}

func s3Store() *odoov1.OdooBackupStore {
	return &odoov1.OdooBackupStore{
		ObjectMeta: metav1.ObjectMeta{Name: "s3", Namespace: ns},
		Spec: odoov1.OdooBackupStoreSpec{
			S3: &odoov1.S3Store{
				Endpoint: "https://minio.example.com/", Region: "eu-central-1", Bucket: "backups",
				Path: "odoo", ForcePathStyle: true,
				CredentialsSecret: corev1.LocalObjectReference{Name: "s3-creds"},
			},
			EncryptionKeySecret: passwordRef("password"),
		},
	}
}

func sftpStore() *odoov1.OdooBackupStore {
	return &odoov1.OdooBackupStore{
		ObjectMeta: metav1.ObjectMeta{Name: "sftp", Namespace: ns},
		Spec: odoov1.OdooBackupStoreSpec{
			SFTP: &odoov1.SFTPStore{
				Host: "backup.example.com", Port: 2222, User: "odoo", Path: "/srv/backups",
				SSHSecret: corev1.LocalObjectReference{Name: "ssh"},
			},
			EncryptionKeySecret: passwordRef("restic"),
		},
	}
}

func nfsStore() *odoov1.OdooBackupStore {
	return &odoov1.OdooBackupStore{
		ObjectMeta: metav1.ObjectMeta{Name: "nfs", Namespace: ns},
		Spec: odoov1.OdooBackupStoreSpec{
			NFS:                 &odoov1.NFSStore{Server: "nfs.example.com", Path: "/exports/backups"},
			EncryptionKeySecret: passwordRef(""),
		},
	}
}

func pvcStore() *odoov1.OdooBackupStore {
	return &odoov1.OdooBackupStore{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc", Namespace: ns},
		Spec: odoov1.OdooBackupStoreSpec{
			PVC:                 &odoov1.PVCStore{ClaimName: "backup-claim", SubPath: "odoo/"},
			EncryptionKeySecret: passwordRef("password"),
		},
	}
}

func allStores() map[string]*odoov1.OdooBackupStore {
	return map[string]*odoov1.OdooBackupStore{"s3": s3Store(), "sftp": sftpStore(), "nfs": nfsStore(), "pvc": pvcStore()}
}

func instance() *odoov1.OdooDeployment {
	od := &odoov1.OdooDeployment{ObjectMeta: metav1.ObjectMeta{Name: instName, Namespace: ns}}
	od.Spec.Database.MaintenanceDatabase = "maint"
	od.Status.AppliedImage = "odoo:18.0"
	od.Status.AppliedUpgradeToken = "tok-1"
	od.Status.OdooDataPvcName = "odoo-data"
	od.Status.OdooConfigSecretName = "odoo-config"
	return od
}

func params(store *odoov1.OdooBackupStore) Params {
	return Params{Image: image, Instance: instance(), Database: "prod", Store: store, OperatorVersion: "v1"}
}

func envMap(env []corev1.EnvVar) map[string]corev1.EnvVar {
	m := map[string]corev1.EnvVar{}
	for _, e := range env {
		m[e.Name] = e
	}
	return m
}

func plain(t *testing.T, env []corev1.EnvVar, name string) string {
	t.Helper()
	e, ok := envMap(env)[name]
	if !ok {
		t.Fatalf("env %s is not set", name)
	}
	if e.ValueFrom != nil {
		t.Fatalf("env %s comes from %v, want a plain value", name, e.ValueFrom)
	}
	return e.Value
}

func secretRef(t *testing.T, env []corev1.EnvVar, name string) (string, string) {
	t.Helper()
	e, ok := envMap(env)[name]
	if !ok {
		t.Fatalf("env %s is not set", name)
	}
	if e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("env %s = %+v, want a secretKeyRef", name, e)
	}
	return e.ValueFrom.SecretKeyRef.Name, e.ValueFrom.SecretKeyRef.Key
}

var sensitiveEnv = regexp.MustCompile(`(?i)(SECRET|PASSWORD|PASS$|ACCESS_KEY|TOKEN|PRIVATE)`)

// checkNoSecretLiterals asserts that credentials only ever reach a
// container through a secretKeyRef or a mounted Secret file.
func checkNoSecretLiterals(t *testing.T, containers []corev1.Container) {
	t.Helper()
	for _, c := range containers {
		for _, e := range c.Env {
			if !sensitiveEnv.MatchString(e.Name) || strings.HasSuffix(e.Name, "_FILE") {
				continue
			}
			if e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
				t.Errorf("container %s: env %s is not a secretKeyRef: %+v", c.Name, e.Name, e)
			}
		}
		for _, e := range c.Env {
			if e.Name == "RESTIC_PASSWORD" || e.Name == "RCLONE_CONFIG_STORE_PASS" {
				t.Errorf("container %s sets %s", c.Name, e.Name)
			}
			if e.Name == "RESTIC_PASSWORD_FILE" && !strings.HasPrefix(e.Value, "/secrets/") {
				t.Errorf("container %s: RESTIC_PASSWORD_FILE = %q", c.Name, e.Value)
			}
		}
	}
}

// checkPodWiring asserts structural invariants of every pod the package renders.
func checkPodWiring(t *testing.T, spec corev1.PodSpec) {
	t.Helper()
	volumes := map[string]corev1.Volume{}
	for _, v := range spec.Volumes {
		if _, dup := volumes[v.Name]; dup {
			t.Errorf("volume %s is declared twice", v.Name)
		}
		volumes[v.Name] = v
	}
	for _, c := range append(append([]corev1.Container(nil), spec.InitContainers...), spec.Containers...) {
		paths := map[string]bool{}
		for _, m := range c.VolumeMounts {
			v, ok := volumes[m.Name]
			if !ok {
				t.Errorf("container %s mounts unknown volume %s", c.Name, m.Name)
			}
			if paths[m.MountPath] {
				t.Errorf("container %s mounts %s twice", c.Name, m.MountPath)
			}
			paths[m.MountPath] = true
			if v.Secret != nil && !m.ReadOnly {
				t.Errorf("container %s mounts Secret volume %s writable", c.Name, m.Name)
			}
		}
		if c.Image != image {
			t.Errorf("container %s runs %s", c.Name, c.Image)
		}
	}
	// The store's credentials are not readable by other users. (odoo.conf
	// is the Odoo pods' own volume, mode 0444, in a one-container pod.)
	for _, v := range spec.Volumes {
		if v.Secret != nil && strings.HasPrefix(v.Name, "store") &&
			(v.Secret.DefaultMode == nil || *v.Secret.DefaultMode&0o007 != 0) {
			t.Errorf("Secret volume %s is readable by others: %v", v.Name, v.Secret.DefaultMode)
		}
	}
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Error("the pod gets a service account token")
	}
	if spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restart policy %s", spec.RestartPolicy)
	}
	if spec.SecurityContext == nil || spec.SecurityContext.RunAsNonRoot == nil || !*spec.SecurityContext.RunAsNonRoot {
		t.Errorf("security context = %+v", spec.SecurityContext)
	}
	if len(spec.Containers) != 1 || spec.Containers[0].Name != ContainerName {
		t.Fatalf("containers = %+v", spec.Containers)
	}
	c := spec.Containers[0]
	if c.TerminationMessagePath != "/dev/termination-log" || c.TerminationMessagePolicy != corev1.TerminationMessageReadFile {
		t.Errorf("termination message %s %s", c.TerminationMessagePath, c.TerminationMessagePolicy)
	}
	if len(c.Command) < 2 || c.Command[0] != "odoo-backup" {
		t.Errorf("command = %q", c.Command)
	}
	checkNoSecretLiterals(t, append(append([]corev1.Container(nil), spec.InitContainers...), spec.Containers...))
}

func checkJobName(t *testing.T, name string) {
	t.Helper()
	if len(name) > 63 {
		t.Errorf("job name %q is %d characters", name, len(name))
	}
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		t.Errorf("job name %q: %v", name, errs)
	}
}

func TestLocateS3(t *testing.T) {
	loc, err := Locate(s3Store(), "shop/odoo")
	if err != nil {
		t.Fatal(err)
	}
	if loc.Repository != "s3:https://minio.example.com/backups/odoo/shop/odoo" {
		t.Errorf("Repository = %q", loc.Repository)
	}
	if loc.ProbeTarget != "store:backups/odoo" {
		t.Errorf("ProbeTarget = %q", loc.ProbeTarget)
	}
	if !reflect.DeepEqual(loc.ResticOptions, []string{"s3.region=eu-central-1", "s3.bucket-lookup=path"}) {
		t.Errorf("ResticOptions = %q", loc.ResticOptions)
	}
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "RCLONE_CONFIG_STORE_ACCESS_KEY_ID"} {
		if s, k := secretRef(t, loc.Env, name); s != "s3-creds" || k != odoov1.S3AccessKeyIDKey {
			t.Errorf("%s from %s/%s", name, s, k)
		}
	}
	for _, name := range []string{"AWS_SECRET_ACCESS_KEY", "RCLONE_CONFIG_STORE_SECRET_ACCESS_KEY"} {
		if s, k := secretRef(t, loc.Env, name); s != "s3-creds" || k != odoov1.S3SecretAccessKeyKey {
			t.Errorf("%s from %s/%s", name, s, k)
		}
	}
	want := map[string]string{
		"RCLONE_CONFIG_STORE_TYPE":             "s3",
		"RCLONE_CONFIG_STORE_PROVIDER":         "Other",
		"RCLONE_CONFIG_STORE_ENDPOINT":         "https://minio.example.com/",
		"AWS_DEFAULT_REGION":                   "eu-central-1",
		"RCLONE_CONFIG_STORE_REGION":           "eu-central-1",
		"RCLONE_CONFIG_STORE_FORCE_PATH_STYLE": "true",
		"RESTIC_PASSWORD_FILE":                 "/secrets/restic/password",
		"RESTIC_REPOSITORY":                    loc.Repository,
	}
	for k, v := range want {
		if got := plain(t, loc.Env, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if len(loc.Volumes) != 1 || loc.Volumes[0].Secret == nil || loc.Volumes[0].Secret.SecretName != "restic-pw" ||
		!reflect.DeepEqual(loc.Volumes[0].Secret.Items, []corev1.KeyToPath{{Key: "password", Path: "password"}}) {
		t.Errorf("volumes = %+v", loc.Volumes)
	}
	if len(loc.Mounts) != 1 || loc.Mounts[0].MountPath != "/secrets/restic" || !loc.Mounts[0].ReadOnly {
		t.Errorf("mounts = %+v", loc.Mounts)
	}
}

func TestLocateS3Variants(t *testing.T) {
	cases := []struct {
		name               string
		mutate             func(*odoov1.S3Store)
		repo, probe        string
		options            []string
		provider, endpoint string
	}{
		{"aws defaults", func(s *odoov1.S3Store) { s.Endpoint, s.Region, s.Path, s.ForcePathStyle = "", "", "", false },
			"s3:s3.amazonaws.com/backups/shop/odoo", "store:backups", nil, "AWS", ""},
		{"region only", func(s *odoov1.S3Store) { s.Endpoint, s.ForcePathStyle = "", false },
			"s3:s3.amazonaws.com/backups/odoo/shop/odoo", "store:backups/odoo", []string{"s3.region=eu-central-1"}, "AWS", ""},
		{"path style only", func(s *odoov1.S3Store) { s.Region = "" },
			"s3:https://minio.example.com/backups/odoo/shop/odoo", "store:backups/odoo", []string{"s3.bucket-lookup=path"},
			"Other", "https://minio.example.com/"},
		{"http endpoint with port", func(s *odoov1.S3Store) { s.Endpoint = "http://minio:9000" },
			"s3:http://minio:9000/backups/odoo/shop/odoo", "store:backups/odoo",
			[]string{"s3.region=eu-central-1", "s3.bucket-lookup=path"}, "Other", "http://minio:9000"},
		{"nested path with slashes", func(s *odoov1.S3Store) { s.Path = "a/b/" },
			"s3:https://minio.example.com/backups/a/b/shop/odoo", "store:backups/a/b",
			[]string{"s3.region=eu-central-1", "s3.bucket-lookup=path"}, "Other", "https://minio.example.com/"},
		// A leading "/" is not part of the key prefix.
		{"leading slash", func(s *odoov1.S3Store) { s.Path = "/a/b" },
			"s3:https://minio.example.com/backups/a/b/shop/odoo", "store:backups/a/b",
			[]string{"s3.region=eu-central-1", "s3.bucket-lookup=path"}, "Other", "https://minio.example.com/"},
		{"root path", func(s *odoov1.S3Store) { s.Path = "/" },
			"s3:https://minio.example.com/backups/shop/odoo", "store:backups",
			[]string{"s3.region=eu-central-1", "s3.bucket-lookup=path"}, "Other", "https://minio.example.com/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := s3Store()
			c.mutate(store.Spec.S3)
			loc, err := Locate(store, "shop/odoo")
			if err != nil {
				t.Fatal(err)
			}
			if loc.Repository != c.repo || loc.ProbeTarget != c.probe {
				t.Errorf("repository %q probe %q, want %q %q", loc.Repository, loc.ProbeTarget, c.repo, c.probe)
			}
			if strings.Contains(strings.TrimPrefix(strings.TrimPrefix(loc.Repository, "s3:https://"), "s3:http://"), "//") {
				t.Errorf("repository %q has an empty path segment", loc.Repository)
			}
			if !reflect.DeepEqual(loc.ResticOptions, c.options) {
				t.Errorf("options = %q, want %q", loc.ResticOptions, c.options)
			}
			if got := plain(t, loc.Env, "RCLONE_CONFIG_STORE_PROVIDER"); got != c.provider {
				t.Errorf("provider = %q", got)
			}
			e, ok := envMap(loc.Env)["RCLONE_CONFIG_STORE_ENDPOINT"]
			if ok != (c.endpoint != "") || e.Value != c.endpoint {
				t.Errorf("endpoint = %+v (set %v)", e, ok)
			}
			if _, ok := envMap(loc.Env)["AWS_DEFAULT_REGION"]; ok != (store.Spec.S3.Region != "") {
				t.Errorf("AWS_DEFAULT_REGION set = %v", ok)
			}
			if _, ok := envMap(loc.Env)["RCLONE_CONFIG_STORE_FORCE_PATH_STYLE"]; ok != store.Spec.S3.ForcePathStyle {
				t.Errorf("FORCE_PATH_STYLE set = %v", ok)
			}
		})
	}
}

func TestS3Repository(t *testing.T) {
	s3 := &odoov1.S3Store{Bucket: "b"}
	if got := S3Repository(s3, ""); got != "s3:s3.amazonaws.com/b" {
		t.Errorf("empty repo = %q", got)
	}
	s3.Path = "p"
	if got := S3Repository(s3, ""); got != "s3:s3.amazonaws.com/b/p" {
		t.Errorf("path only = %q", got)
	}
}

func TestLocateSFTP(t *testing.T) {
	loc, err := Locate(sftpStore(), "shop/odoo")
	if err != nil {
		t.Fatal(err)
	}
	if loc.Repository != "rclone:store:/srv/backups/shop/odoo" || loc.ProbeTarget != "store:/srv/backups" {
		t.Errorf("repository %q probe %q", loc.Repository, loc.ProbeTarget)
	}
	want := map[string]string{
		"RCLONE_CONFIG_STORE_TYPE":              "sftp",
		"RCLONE_CONFIG_STORE_HOST":              "backup.example.com",
		"RCLONE_CONFIG_STORE_PORT":              "2222",
		"RCLONE_CONFIG_STORE_USER":              "odoo",
		"RCLONE_CONFIG_STORE_KEY_FILE":          "/secrets/ssh/id_ed25519",
		"RCLONE_CONFIG_STORE_KNOWN_HOSTS_FILE":  "/secrets/ssh/known_hosts",
		"RCLONE_CONFIG_STORE_SHELL_TYPE":        "none",
		"RCLONE_CONFIG_STORE_DISABLE_HASHCHECK": "true",
		"RESTIC_REPOSITORY":                     loc.Repository,
	}
	for k, v := range want {
		if got := plain(t, loc.Env, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if len(loc.ResticOptions) != 0 {
		t.Errorf("options = %q", loc.ResticOptions)
	}
	var ssh *corev1.Volume
	for i := range loc.Volumes {
		if loc.Volumes[i].Name == "store-ssh" {
			ssh = &loc.Volumes[i]
		}
	}
	if ssh == nil || ssh.Secret.SecretName != "ssh" || len(ssh.Secret.Items) != 3 {
		t.Fatalf("ssh volume = %+v", ssh)
	}
	// The private key is under either name, so neither may be required.
	if ssh.Secret.Optional == nil || !*ssh.Secret.Optional ||
		!slices.ContainsFunc(ssh.Secret.Items, func(k corev1.KeyToPath) bool {
			return k.Key == "ssh-privatekey" && k.Path == "ssh-privatekey"
		}) {
		t.Errorf("ssh volume = %+v", ssh.Secret)
	}
	if !slices.ContainsFunc(loc.Mounts, func(m corev1.VolumeMount) bool {
		return m.Name == "store-ssh" && m.MountPath == "/secrets/ssh" && m.ReadOnly
	}) {
		t.Errorf("mounts = %+v", loc.Mounts)
	}
	// The password key defaults to the item path "password".
	pw := loc.Volumes[len(loc.Volumes)-1]
	if pw.Secret.Items[0].Key != "restic" || pw.Secret.Items[0].Path != "password" {
		t.Errorf("password item = %+v", pw.Secret.Items)
	}

	s := sftpStore()
	s.Spec.SFTP.Port, s.Spec.SFTP.Path = 0, ""
	loc, err = Locate(s, "shop/odoo")
	if err != nil {
		t.Fatal(err)
	}
	if plain(t, loc.Env, "RCLONE_CONFIG_STORE_PORT") != "22" {
		t.Error("default port is not 22")
	}
	if loc.Repository != "rclone:store:shop/odoo" || loc.ProbeTarget != "store:" {
		t.Errorf("relative: repository %q probe %q", loc.Repository, loc.ProbeTarget)
	}
	s.Spec.SFTP.Path = "rel/dir/"
	loc, _ = Locate(s, "shop/odoo")
	if loc.Repository != "rclone:store:rel/dir/shop/odoo" || loc.ProbeTarget != "store:rel/dir" {
		t.Errorf("relative path: repository %q probe %q", loc.Repository, loc.ProbeTarget)
	}
}

func TestLocateNFS(t *testing.T) {
	loc, err := Locate(nfsStore(), "shop/odoo")
	if err != nil {
		t.Fatal(err)
	}
	if loc.Repository != "/store/shop/odoo" || loc.ProbeTarget != "/store" {
		t.Errorf("repository %q probe %q", loc.Repository, loc.ProbeTarget)
	}
	v := loc.Volumes[0]
	if v.Name != "store" || v.NFS == nil || v.NFS.Server != "nfs.example.com" || v.NFS.Path != "/exports/backups" || v.NFS.ReadOnly {
		t.Errorf("nfs volume = %+v", v)
	}
	if loc.Mounts[0].MountPath != "/store" || loc.Mounts[0].ReadOnly {
		t.Errorf("store mount = %+v", loc.Mounts[0])
	}
	// An empty key means the item "password".
	pw := loc.Volumes[len(loc.Volumes)-1]
	if pw.Secret.Items[0].Key != "password" {
		t.Errorf("password item = %+v", pw.Secret.Items)
	}
	for _, e := range loc.Env {
		if strings.HasPrefix(e.Name, "RCLONE_") || strings.HasPrefix(e.Name, "AWS_") {
			t.Errorf("nfs sets %s", e.Name)
		}
	}
}

func TestLocatePVC(t *testing.T) {
	cases := map[string]struct{ repo, probe string }{
		"":          {"/store/shop/odoo", "/store"},
		"odoo/":     {"/store/odoo/shop/odoo", "/store/odoo"},
		"/odoo":     {"/store/odoo/shop/odoo", "/store/odoo"},
		"a/b":       {"/store/a/b/shop/odoo", "/store/a/b"},
		"a//b///":   {"/store/a/b/shop/odoo", "/store/a/b"},
		"./backups": {"/store/backups/shop/odoo", "/store/backups"},
	}
	for sub, want := range cases {
		s := pvcStore()
		s.Spec.PVC.SubPath = odoov1.StorePath(sub)
		loc, err := Locate(s, "shop/odoo")
		if err != nil {
			t.Fatal(err)
		}
		if loc.Repository != want.repo || loc.ProbeTarget != want.probe {
			t.Errorf("subPath %q: repository %q probe %q, want %q %q", sub, loc.Repository, loc.ProbeTarget, want.repo, want.probe)
		}
		v := loc.Volumes[0]
		if v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ClaimName != "backup-claim" {
			t.Errorf("volume = %+v", v)
		}
		if loc.Mounts[0].SubPath != "" {
			t.Errorf("the store is mounted with a subPath %q", loc.Mounts[0].SubPath)
		}
	}
	// "/" names the claim's root.
	s := pvcStore()
	s.Spec.PVC.SubPath = "/"
	loc, _ := Locate(s, "shop/odoo")
	if !strings.HasPrefix(loc.Repository, "/store/") || !strings.HasSuffix(loc.Repository, "/shop/odoo") {
		t.Errorf("subPath /: repository %q", loc.Repository)
	}
}

func TestLocateProbeHasNoRepository(t *testing.T) {
	for name, store := range allStores() {
		loc, err := Locate(store, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := envMap(loc.Env)["RESTIC_REPOSITORY"]; ok {
			t.Errorf("%s: a probe gets RESTIC_REPOSITORY", name)
		}
		if loc.ProbeTarget == "" {
			t.Errorf("%s: no probe target", name)
		}
		if _, ok := envMap(loc.Env)["RESTIC_PASSWORD_FILE"]; !ok {
			t.Errorf("%s: no password file", name)
		}
	}
}

func TestLocateInvalid(t *testing.T) {
	store := &odoov1.OdooBackupStore{ObjectMeta: metav1.ObjectMeta{Name: "empty"}}
	if _, err := Locate(store, "x"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("Locate(empty) = %v", err)
	}
	p := params(store)
	if _, err := BackupJob(p, &odoov1.OdooBackup{ObjectMeta: metav1.ObjectMeta{Name: "b"}}, "manual", 0, false); err == nil {
		t.Error("BackupJob with an empty store succeeded")
	}
	if _, err := RestoreJob(p, &odoov1.OdooRestore{ObjectMeta: metav1.ObjectMeta{Name: "r"}}, "s", "snap", "", nil, 0); err == nil {
		t.Error("RestoreJob with an empty store succeeded")
	}
	if _, err := ProbeJob(image, store, "h"); err == nil {
		t.Error("ProbeJob with an empty store succeeded")
	}
}

func TestStoreSecrets(t *testing.T) {
	cases := map[string]map[string][]string{
		"s3":   {"s3-creds": {"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}, "restic-pw": {"password"}},
		"sftp": {"ssh": {"id_ed25519|ssh-privatekey", "known_hosts"}, "restic-pw": {"restic"}},
		"nfs":  {"restic-pw": {"password"}},
		"pvc":  {"restic-pw": {"password"}},
	}
	stores := allStores()
	for name, want := range cases {
		if got := StoreSecrets(stores[name]); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: StoreSecrets = %v, want %v", name, got, want)
		}
	}
	// One Secret for everything: the keys add up.
	s := s3Store()
	s.Spec.S3.CredentialsSecret.Name = "restic-pw"
	got := StoreSecrets(s)
	if !reflect.DeepEqual(got, map[string][]string{"restic-pw": {"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "password"}}) {
		t.Errorf("shared Secret = %v", got)
	}
	// Unnamed Secrets are left out.
	s = nfsStore()
	s.Spec.EncryptionKeySecret.Name = ""
	if got := StoreSecrets(s); len(got) != 0 {
		t.Errorf("unnamed = %v", got)
	}
}

func TestJobNames(t *testing.T) {
	od := instance()
	names := map[string]string{
		"backup":  BackupJobName(od, "nightly", 0),
		"restore": RestoreJobName(od, "nightly", 0),
		"cleanup": CleanupJobName(od, "nightly", 0),
		"probe":   ProbeJobName(s3Store(), "hash"),
	}
	seen := map[string]bool{}
	for kind, n := range names {
		checkJobName(t, n)
		if seen[n] {
			t.Errorf("%s: name %s is not unique", kind, n)
		}
		seen[n] = true
	}
	if !regexp.MustCompile(`^odoo-backup-[0-9a-f]{8}-0$`).MatchString(names["backup"]) {
		t.Errorf("backup job name %q", names["backup"])
	}
	if !regexp.MustCompile(`^s3-probe-[0-9a-f]{8}-0$`).MatchString(names["probe"]) {
		t.Errorf("probe job name %q", names["probe"])
	}
	// Deterministic, and different for each input.
	if BackupJobName(od, "nightly", 0) != names["backup"] {
		t.Error("not deterministic")
	}
	other := instance()
	other.Namespace = "other"
	variants := []string{
		BackupJobName(od, "nightly", 1), BackupJobName(od, "nightly2", 0), BackupJobName(other, "nightly", 0),
	}
	for _, v := range variants {
		if v == names["backup"] {
			t.Errorf("%s collides", v)
		}
	}
	if ProbeJobName(s3Store(), "a") == ProbeJobName(s3Store(), "b") {
		t.Error("probe names ignore the hash")
	}
}

func TestJobNamesLongAndOdd(t *testing.T) {
	od := instance()
	for _, name := range []string{
		strings.Repeat("a", 63),
		strings.Repeat("a", 40) + "-" + strings.Repeat("b", 22),
		strings.Repeat("a", 44) + "-.-" + strings.Repeat("b", 16), // the cut lands on separators
		strings.Repeat("a", 45) + "." + strings.Repeat("b", 17),
		"a",
	} {
		od.Name = name
		for attempt := int32(0); attempt < 3; attempt++ {
			for _, n := range []string{
				BackupJobName(od, "b", attempt), RestoreJobName(od, "r", attempt), CleanupJobName(od, "r", attempt),
				BackupJobName(od, "b", 2147483647),
			} {
				checkJobName(t, n)
			}
		}
		store := s3Store()
		store.Name = name
		checkJobName(t, ProbeJobName(store, strings.Repeat("h", 64)))
	}
	od.Name = strings.Repeat("a", 63)
	if BackupJobName(od, "x", 0) == BackupJobName(od, "y", 0) {
		t.Error("long names collide")
	}
}

func command(job batchv1.Job) []string { return job.Spec.Template.Spec.Containers[0].Command }

func flagValue(t *testing.T, cmd []string, flag string) string {
	t.Helper()
	i := slices.Index(cmd, flag)
	if i < 0 || i+1 >= len(cmd) {
		t.Fatalf("command %q has no %s", cmd, flag)
	}
	return cmd[i+1]
}

func mount(t *testing.T, c corev1.Container, path string) corev1.VolumeMount {
	t.Helper()
	for _, m := range c.VolumeMounts {
		if m.MountPath == path {
			return m
		}
	}
	t.Fatalf("container %s mounts nothing at %s: %+v", c.Name, path, c.VolumeMounts)
	return corev1.VolumeMount{}
}

func checkBackupCommand(t *testing.T, job batchv1.Job, store *odoov1.OdooBackupStore) {
	t.Helper()
	cmd := command(job)
	if cmd[1] != "backup" {
		t.Errorf("subcommand %q", cmd[1])
	}
	flags := map[string]string{
		"--namespace": ns, "--deployment": instName, "--backup": "nightly", "--kind": "scheduled",
		"--image": "odoo:18.0", "--upgrade-token": "tok-1", "--consistency": "Online", "--root": "/backup",
		"--maintenance-db": "maint",
	}
	for f, v := range flags {
		if got := flagValue(t, cmd, f); got != v {
			t.Errorf("%s = %q, want %q", f, got, v)
		}
	}
	if !slices.Contains(cmd, "--wait-for-cleanup") {
		t.Error("no --wait-for-cleanup")
	}
	loc, _ := Locate(store, "shop/odoo")
	var opts []string
	for i, a := range cmd {
		if a == "--restic-option" {
			opts = append(opts, cmd[i+1])
		}
	}
	if !slices.Equal(opts, loc.ResticOptions) {
		t.Errorf("restic options %q, want %q", opts, loc.ResticOptions)
	}
}

func checkBackupMounts(t *testing.T, spec corev1.PodSpec, store *odoov1.OdooBackupStore) {
	t.Helper()
	loc, _ := Locate(store, "shop/odoo")
	c := spec.Containers[0]
	if got := plain(t, c.Env, "RESTIC_REPOSITORY"); got != loc.Repository {
		t.Errorf("RESTIC_REPOSITORY = %q", got)
	}
	for k, v := range map[string]string{"HOME": "/tmp", "TMPDIR": "/tmp", "RESTIC_CACHE_DIR": "/tmp/restic-cache"} {
		if plain(t, c.Env, k) != v {
			t.Errorf("%s != %s", k, v)
		}
	}
	fsMount := mount(t, c, "/backup/filestore")
	if !fsMount.ReadOnly || fsMount.SubPath != "filestore/prod" || fsMount.Name != "odoo-data" {
		t.Errorf("filestore mount = %+v", fsMount)
	}
	if m := mount(t, c, "/etc/odoo"); !m.ReadOnly {
		t.Error("odoo.conf is writable")
	}
	if m := mount(t, c, "/backup"); m.ReadOnly || m.Name != "scratch" {
		t.Errorf("scratch mount = %+v", m)
	}
	for _, m := range c.VolumeMounts {
		if m.Name == "odoo-data" && m.MountPath != "/backup/filestore" {
			t.Errorf("the backup container mounts the data volume at %s", m.MountPath)
		}
	}
	if len(spec.InitContainers) != 1 {
		t.Fatalf("init containers = %+v", spec.InitContainers)
	}
	ic := spec.InitContainers[0]
	if !slices.Equal(ic.Command, []string{"odoo-backup", "prepare", "--data", "/data", "--database", "prod"}) {
		t.Errorf("prepare command = %q", ic.Command)
	}
	if len(ic.Env) != 0 {
		t.Errorf("prepare gets env %v", ic.Env)
	}
	if len(ic.VolumeMounts) != 1 || ic.VolumeMounts[0].Name != "odoo-data" || ic.VolumeMounts[0].MountPath != "/data" {
		t.Errorf("prepare mounts %+v", ic.VolumeMounts)
	}
	var pvc *corev1.Volume
	for i := range spec.Volumes {
		if spec.Volumes[i].Name == "odoo-data" {
			pvc = &spec.Volumes[i]
		}
	}
	if pvc == nil || pvc.PersistentVolumeClaim.ClaimName != "odoo-data" {
		t.Errorf("data volume = %+v", pvc)
	}
}

func TestBackupJob(t *testing.T) {
	for name, store := range allStores() {
		t.Run(name, func(t *testing.T) {
			p := params(store)
			p.Affinity = &corev1.Affinity{PodAffinity: &corev1.PodAffinity{}}
			backup := &odoov1.OdooBackup{ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns}}
			job, err := BackupJob(p, backup, "scheduled", 2, true)
			if err != nil {
				t.Fatal(err)
			}
			spec := job.Spec.Template.Spec
			checkPodWiring(t, spec)
			if job.Name != BackupJobName(p.Instance, "nightly", 2) || job.Namespace != ns {
				t.Errorf("job %s/%s", job.Namespace, job.Name)
			}
			wantLabels := map[string]string{
				odoov1.LabelOdooDeployment: instName, odoov1.LabelJobKind: KindBackup, LabelBackup: "nightly",
			}
			if !reflect.DeepEqual(job.Labels, wantLabels) || !reflect.DeepEqual(job.Spec.Template.Labels, wantLabels) {
				t.Errorf("labels %v / %v", job.Labels, job.Spec.Template.Labels)
			}
			if *job.Spec.BackoffLimit != 1 || *job.Spec.ActiveDeadlineSeconds != 21600 || *job.Spec.TTLSecondsAfterFinished != 86400 ||
				*job.Spec.Parallelism != 1 || *job.Spec.Completions != 1 {
				t.Errorf("job spec %+v", job.Spec)
			}
			if spec.Affinity != p.Affinity {
				t.Error("affinity not passed through")
			}
			checkBackupCommand(t, job, store)
			checkBackupMounts(t, spec, store)
		})
	}
}

func TestBackupJobOptions(t *testing.T) {
	p := params(nfsStore())
	ttl := int32(60)
	deadline := int64(120)
	p.Instance.Spec.Jobs.TTLSecondsAfterFinished = &ttl
	p.Instance.Spec.Backup.ActiveDeadlineSeconds = &deadline
	p.Instance.Spec.Backup.Consistency = odoov1.BackupConsistencyOffline
	p.Instance.Spec.Database.MaintenanceDatabase = ""
	p.Instance.Spec.PodSecurityContext = &corev1.PodSecurityContext{RunAsUser: ptr(int64(1000)), RunAsNonRoot: ptr(true)}
	limits := corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("8Gi")}}
	p.Instance.Spec.Backup.Resources = &limits
	job, err := BackupJob(p, &odoov1.OdooBackup{ObjectMeta: metav1.ObjectMeta{Name: "b"}}, "manual", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if *job.Spec.TTLSecondsAfterFinished != 60 || *job.Spec.ActiveDeadlineSeconds != 120 {
		t.Errorf("ttl %d deadline %d", *job.Spec.TTLSecondsAfterFinished, *job.Spec.ActiveDeadlineSeconds)
	}
	if *job.Spec.BackoffLimit != 1 {
		t.Errorf("backoff %d, want 1 for a backup Odoo serves through", *job.Spec.BackoffLimit)
	}
	p.Offline = true
	offline, err := BackupJob(p, &odoov1.OdooBackup{ObjectMeta: metav1.ObjectMeta{Name: "b"}}, "manual", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if *offline.Spec.BackoffLimit != 0 {
		t.Errorf("backoff %d, want 0 while Odoo is stopped", *offline.Spec.BackoffLimit)
	}
	cmd := command(job)
	if slices.Contains(cmd, "--wait-for-cleanup") || flagValue(t, cmd, "--consistency") != "Offline" ||
		flagValue(t, cmd, "--maintenance-db") != "postgres" {
		t.Errorf("command = %q", cmd)
	}
	spec := job.Spec.Template.Spec
	if *spec.SecurityContext.RunAsUser != 1000 {
		t.Errorf("security context = %+v", spec.SecurityContext)
	}
	if !reflect.DeepEqual(spec.Containers[0].Resources, limits) {
		t.Errorf("resources = %+v", spec.Containers[0].Resources)
	}
	// The instance's resources are copied, not shared.
	spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("1Mi")
	if p.Instance.Spec.Backup.Resources.Limits.Memory().String() != "8Gi" {
		t.Error("the job shares the instance's resource list")
	}
	if spec.Affinity != nil {
		t.Error("affinity set without one")
	}
	if *spec.SecurityContext.RunAsUser == *p.Instance.Spec.PodSecurityContext.RunAsUser {
		spec.SecurityContext.RunAsUser = ptr(int64(5))
		if *p.Instance.Spec.PodSecurityContext.RunAsUser != 1000 {
			t.Error("the job shares the instance's security context")
		}
	}
}

func TestDefaultResources(t *testing.T) {
	job := CleanupJob(params(nil), &odoov1.OdooRestore{ObjectMeta: metav1.ObjectMeta{Name: "r"}}, "20250101T000000", 0)
	res := job.Spec.Template.Spec.Containers[0].Resources
	if res.Requests.Cpu().String() != "100m" || res.Requests.Memory().String() != "256Mi" || res.Limits.Memory().String() != "2Gi" {
		t.Errorf("default resources = %+v", res)
	}
}

func TestRestoreJob(t *testing.T) {
	for name, store := range allStores() {
		t.Run(name, func(t *testing.T) {
			p := params(store)
			rs := &odoov1.OdooRestore{ObjectMeta: metav1.ObjectMeta{Name: "rs1"}}
			job, err := RestoreJob(p, rs, "20250101T000000", "abcd1234", "", []string{"sale", "stock"}, 1)
			if err != nil {
				t.Fatal(err)
			}
			spec := job.Spec.Template.Spec
			checkPodWiring(t, spec)
			cmd := command(job)
			if cmd[1] != "restore" || flagValue(t, cmd, "--snapshot") != "abcd1234" || flagValue(t, cmd, "--suffix") != "20250101T000000" ||
				flagValue(t, cmd, "--data") != "/data" || flagValue(t, cmd, "--expect-modules") != "sale,stock" ||
				flagValue(t, cmd, "--maintenance-db") != "maint" || slices.Contains(cmd, "--undo-suffix") {
				t.Errorf("command = %q", cmd)
			}
			if job.Name != RestoreJobName(p.Instance, "rs1", 1) || job.Labels[LabelRestore] != "rs1" || job.Labels[odoov1.LabelJobKind] != KindRestore {
				t.Errorf("job %s labels %v", job.Name, job.Labels)
			}
			if *job.Spec.BackoffLimit != 2 || *job.Spec.ActiveDeadlineSeconds != 21600 {
				t.Errorf("backoff %d deadline %d", *job.Spec.BackoffLimit, *job.Spec.ActiveDeadlineSeconds)
			}
			c := spec.Containers[0]
			if m := mount(t, c, "/data"); m.ReadOnly || m.SubPath != "" || m.Name != "odoo-data" {
				t.Errorf("data mount = %+v", m)
			}
			loc, _ := Locate(store, "shop/odoo")
			if plain(t, c.Env, "RESTIC_REPOSITORY") != loc.Repository {
				t.Error("RESTIC_REPOSITORY")
			}
			if len(spec.InitContainers) != 0 {
				t.Error("restore has init containers")
			}
		})
	}
}

func TestRestoreJobUndo(t *testing.T) {
	p := params(nil) // an undo needs no store
	rs := &odoov1.OdooRestore{ObjectMeta: metav1.ObjectMeta{Name: "undo1"}}
	job, err := RestoreJob(p, rs, "20250102T000000", "", "20250101T000000", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	spec := job.Spec.Template.Spec
	checkPodWiring(t, spec)
	cmd := command(job)
	if cmd[1] != "undo" || flagValue(t, cmd, "--undo-suffix") != "20250101T000000" || slices.Contains(cmd, "--snapshot") ||
		flagValue(t, cmd, "--suffix") != "20250102T000000" || flagValue(t, cmd, "--expect-modules") != "" {
		t.Errorf("command = %q", cmd)
	}
	for _, e := range spec.Containers[0].Env {
		if strings.HasPrefix(e.Name, "RESTIC_REPOSITORY") || strings.HasPrefix(e.Name, "AWS_") || strings.HasPrefix(e.Name, "RCLONE_") {
			t.Errorf("undo gets store env %s", e.Name)
		}
	}
	for _, v := range spec.Volumes {
		if strings.HasPrefix(v.Name, "store") {
			t.Errorf("undo mounts store volume %s", v.Name)
		}
	}
	if slices.Contains(cmd, "--restic-option") {
		t.Error("undo gets restic options")
	}
}

func TestCleanupJob(t *testing.T) {
	p := params(s3Store())
	rs := &odoov1.OdooRestore{ObjectMeta: metav1.ObjectMeta{Name: "rs1"}}
	job := CleanupJob(p, rs, "20250101T000000", 3)
	spec := job.Spec.Template.Spec
	checkPodWiring(t, spec)
	cmd := command(job)
	want := []string{"odoo-backup", "cleanup", "--suffix", "20250101T000000", "--data", "/data", "--maintenance-db", "maint"}
	if !slices.Equal(cmd, want) {
		t.Errorf("command = %q", cmd)
	}
	if job.Name != CleanupJobName(p.Instance, "rs1", 3) || job.Labels[odoov1.LabelJobKind] != KindCleanup || job.Labels[LabelRestore] != "rs1" {
		t.Errorf("job %s labels %v", job.Name, job.Labels)
	}
	if *job.Spec.BackoffLimit != 2 || *job.Spec.ActiveDeadlineSeconds != 3600 {
		t.Errorf("backoff %d deadline %d", *job.Spec.BackoffLimit, *job.Spec.ActiveDeadlineSeconds)
	}
	for _, e := range spec.Containers[0].Env {
		if strings.HasPrefix(e.Name, "RESTIC_") && e.Name != "RESTIC_CACHE_DIR" {
			t.Errorf("cleanup gets %s", e.Name)
		}
	}
	if len(spec.Volumes) != 3 {
		t.Errorf("volumes = %+v", spec.Volumes)
	}
}

func TestProbeJob(t *testing.T) {
	for name, store := range allStores() {
		t.Run(name, func(t *testing.T) {
			job, err := ProbeJob(image, store, "abc")
			if err != nil {
				t.Fatal(err)
			}
			spec := job.Spec.Template.Spec
			checkPodWiring(t, spec)
			loc, _ := Locate(store, "")
			if !slices.Equal(command(job), []string{"odoo-backup", "probe", "--target", loc.ProbeTarget}) {
				t.Errorf("command = %q", command(job))
			}
			if job.Name != ProbeJobName(store, "abc") || job.Namespace != ns {
				t.Errorf("job %s/%s", job.Namespace, job.Name)
			}
			want := map[string]string{odoov1.LabelJobKind: KindProbe, LabelStore: store.Name}
			if !reflect.DeepEqual(job.Labels, want) || !reflect.DeepEqual(job.Spec.Template.Labels, want) {
				t.Errorf("labels %v", job.Labels)
			}
			if *job.Spec.BackoffLimit != 0 || *job.Spec.ActiveDeadlineSeconds != 300 || *job.Spec.TTLSecondsAfterFinished != 3600 {
				t.Errorf("job spec %+v", job.Spec)
			}
			sc := spec.SecurityContext
			if *sc.RunAsUser != 100 || *sc.RunAsGroup != 101 || *sc.FSGroup != 101 {
				t.Errorf("security context %+v", sc)
			}
			if _, ok := envMap(spec.Containers[0].Env)["RESTIC_REPOSITORY"]; ok {
				t.Error("probe gets RESTIC_REPOSITORY")
			}
			res := spec.Containers[0].Resources
			if res.Limits.Memory().String() != "256Mi" {
				t.Errorf("resources %+v", res)
			}
			if spec.Affinity != nil {
				t.Error("probe has affinity")
			}
		})
	}
	store := pvcStore()
	custom := corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")}}
	store.Spec.Resources = &custom
	job, _ := ProbeJob(image, store, "abc")
	if !reflect.DeepEqual(job.Spec.Template.Spec.Containers[0].Resources, custom) {
		t.Error("store resources ignored")
	}
}

func TestReachJob(t *testing.T) {
	inst := params(nil).Instance
	for name, store := range allStores() {
		t.Run(name, func(t *testing.T) {
			b := &odoov1.OdooBackup{ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns, UID: "u1"}}
			job, err := ReachJob(image, store, inst, b)
			if err != nil {
				t.Fatal(err)
			}
			probe, _ := ProbeJob(image, store, "")
			if !slices.Equal(command(job), command(probe)) {
				t.Errorf("command = %q, want the probe's %q", command(job), command(probe))
			}
			if !reflect.DeepEqual(job.Spec.Template.Spec, probe.Spec.Template.Spec) {
				t.Error("pod differs from the store probe's")
			}
			if job.Name != ReachJobName(inst, b) || job.Namespace != ns {
				t.Errorf("job %s/%s", job.Namespace, job.Name)
			}
			checkJobName(t, job.Name)
			want := map[string]string{odoov1.LabelJobKind: KindReach, LabelStore: store.Name, LabelBackup: "nightly"}
			if !reflect.DeepEqual(job.Labels, want) || !reflect.DeepEqual(job.Spec.Template.Labels, want) {
				t.Errorf("labels %v / %v", job.Labels, job.Spec.Template.Labels)
			}
			if *job.Spec.BackoffLimit != 0 || *job.Spec.ActiveDeadlineSeconds != 300 || *job.Spec.TTLSecondsAfterFinished != 3600 {
				t.Errorf("job spec %+v", job.Spec)
			}
		})
	}
	// A backup taken again under the same name gets a fresh check.
	a := &odoov1.OdooBackup{ObjectMeta: metav1.ObjectMeta{Name: "b", UID: "1"}}
	b := &odoov1.OdooBackup{ObjectMeta: metav1.ObjectMeta{Name: "b", UID: "2"}}
	if ReachJobName(inst, a) == ReachJobName(inst, b) {
		t.Error("the reach Job name ignores the backup's uid")
	}
}

// Every Job of every store: the invariants hold for all combinations.
func TestAllJobsInvariants(t *testing.T) {
	for name, store := range allStores() {
		p := params(store)
		b := &odoov1.OdooBackup{ObjectMeta: metav1.ObjectMeta{Name: "b"}}
		rs := &odoov1.OdooRestore{ObjectMeta: metav1.ObjectMeta{Name: "r"}}
		jobs := make([]batchv1.Job, 0, 6)
		for _, wait := range []bool{false, true} {
			j, err := BackupJob(p, b, "manual", 0, wait)
			if err != nil {
				t.Fatal(err)
			}
			jobs = append(jobs, j)
		}
		for _, undo := range []string{"", "20250101T000000"} {
			j, err := RestoreJob(p, rs, "20250102T000000", "snap", undo, []string{"a"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			jobs = append(jobs, j)
		}
		jobs = append(jobs, CleanupJob(p, rs, "20250102T000000", 0))
		pj, err := ProbeJob(image, store, "h")
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, pj)
		for _, j := range jobs {
			t.Run(fmt.Sprintf("%s/%s", name, j.Name), func(t *testing.T) {
				checkJobName(t, j.Name)
				checkPodWiring(t, j.Spec.Template.Spec)
				if j.Spec.Template.Spec.Containers[0].ImagePullPolicy != corev1.PullIfNotPresent {
					t.Error("pull policy")
				}
				if !reflect.DeepEqual(j.Labels, j.Spec.Template.Labels) {
					t.Error("job and pod labels differ")
				}
			})
		}
	}
}
