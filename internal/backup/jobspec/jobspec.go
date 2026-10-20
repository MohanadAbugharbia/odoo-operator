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

// Package jobspec renders the backup, restore, cleanup and probe Jobs. The
// Jobs run the odoo-backup tool from the backup image; everything they need
// (odoo.conf, the store's credentials, the data volume) is mounted, and they
// get no Kubernetes credentials: the outcome comes back through the
// termination message.
package jobspec

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
)

// Job kinds, in the LabelJobKind label.
const (
	KindBackup  = "backup"
	KindRestore = "restore"
	KindCleanup = "cleanup"
	KindProbe   = "store-probe"
	KindReach   = "store-reach"
	KindCheck   = "store-check"
	KindPrune   = "store-prune"
	KindDrill   = "drill"
)

// Labels on backup Jobs.
const (
	// LabelBackup names the OdooBackup a backup Job takes.
	LabelBackup = "odoo.abugharbia.com/backup"
	// LabelRestore names the OdooRestore a restore or cleanup Job serves.
	LabelRestore = "odoo.abugharbia.com/restore"
	// LabelStore names the OdooBackupStore a probe Job checks.
	LabelStore = "odoo.abugharbia.com/backup-store"
)

// Container and volume names.
const (
	ContainerName   = "backup"
	volConfig       = "odoo-config"
	volData         = "odoo-data"
	volScratch      = "scratch"
	volDrill        = "drill"
	volTmp          = "tmp"
	tmpDir          = "/tmp"
	volStore        = "store"
	volStorePass    = "store-password"
	volStoreSSH     = "store-ssh"
	rcloneRemote    = "store"
	passwordFile    = "password"
	sshMount        = layout.SecretsMount + "/ssh"
	passwordMount   = layout.SecretsMount + "/restic"
	toolBinary      = "odoo-backup"
	defaultTTL      = int32(86400)
	backupBackoff   = int32(1)
	restoreBackoff  = int32(2)
	cleanupBackoff  = int32(2)
	drillBackoff    = int32(0)
	probeDeadline   = int64(300)
	reachTTL        = int32(3600)
	cleanupDeadline = int64(3600)
	// storeTaskDeadline bounds a check or prune of a whole store.
	storeTaskDeadline = int64(6 * 3600)
)

// Location is how a Job reaches one store: environment, volumes and restic
// options, plus the repository of the instance (empty for a probe).
type Location struct {
	Env           []corev1.EnvVar
	Volumes       []corev1.Volume
	Mounts        []corev1.VolumeMount
	ResticOptions []string
	// Repository is RESTIC_REPOSITORY.
	Repository string
	// ProbeTarget is the store root as rclone names it.
	ProbeTarget string
}

func secretEnv(name, secret, key string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secret},
			Key:                  key,
		}},
	}
}

func plainEnv(name, value string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, Value: value}
}

func rcloneEnvFor(remote, option, value string) corev1.EnvVar {
	return plainEnv("RCLONE_CONFIG_"+strings.ToUpper(remote)+"_"+option, value)
}

// slot names what one store uses inside a Job's pod. The store a Job reads
// or writes uses the plain names (RESTIC_REPOSITORY, the "store" rclone
// remote, /store); each store a backup is copied to has its own (copy1:
// COPY1_RESTIC_REPOSITORY, the "copy1" remote, /store-copy1), so stores with
// different credentials never share a variable.
type slot struct {
	env, remote, storeMount, sshMount, passMount, volStore, volPass, volSSH string
	// rcloneS3 reaches an S3 store through rclone, whose remote carries its
	// own keys, instead of restic's s3 backend, which reads AWS_* and so
	// can serve only one set of keys per process.
	rcloneS3 bool
}

var primarySlot = slot{
	remote: rcloneRemote, storeMount: layout.StoreMount, sshMount: sshMount, passMount: passwordMount,
	volStore: volStore, volPass: volStorePass, volSSH: volStoreSSH,
}

// CopySlot is the name prefix of the n-th (from 1) store a backup is copied to.
func CopySlot(n int) string { return fmt.Sprintf("copy%d", n) }

func copySlot(n int) slot {
	name := CopySlot(n)
	return slot{
		env: strings.ToUpper(name) + "_", remote: name,
		storeMount: layout.StoreMount + "-" + name, sshMount: sshMount + "-" + name, passMount: passwordMount + "-" + name,
		volStore: volStore + "-" + name, volPass: volStorePass + "-" + name, volSSH: volStoreSSH + "-" + name,
		rcloneS3: true,
	}
}

// S3Repository is the restic repository URL of repo inside an S3 store.
func S3Repository(s3 *odoov1.S3Store, repo string) string {
	endpoint := strings.TrimSuffix(s3.Endpoint, "/")
	if endpoint == "" {
		endpoint = "s3.amazonaws.com"
	}
	// A key prefix has no leading "/" (restic would keep one in the keys).
	p := strings.TrimPrefix(layout.JoinStorePath(string(s3.Path), repo), "/")
	url := "s3:" + endpoint + "/" + s3.Bucket
	if p != "" {
		url += "/" + p
	}
	return url
}

// Locate returns how a Job reaches store; repo is the instance's folder
// (layout.Repository), or "" for a probe of the store root.
func Locate(store *odoov1.OdooBackupStore, repo string) (Location, error) {
	return locate(store, repo, primarySlot)
}

// LocateCopy returns how a backup Job reaches store as the n-th (from 1)
// store it copies the backup to.
func LocateCopy(store *odoov1.OdooBackupStore, repo string, n int) (Location, error) {
	return locate(store, repo, copySlot(n))
}

//nolint:gocyclo // one case per store type
func locate(store *odoov1.OdooBackupStore, repo string, sl slot) (Location, error) {
	spec := &store.Spec
	loc := Location{}
	rcloneEnv := func(option, value string) corev1.EnvVar { return rcloneEnvFor(sl.remote, option, value) }
	rcloneSecretEnv := func(option, secret, key string) corev1.EnvVar {
		return secretEnv("RCLONE_CONFIG_"+strings.ToUpper(sl.remote)+"_"+option, secret, key)
	}
	switch {
	case spec.S3 != nil:
		s3 := spec.S3
		creds := s3.CredentialsSecret.Name
		if !sl.rcloneS3 {
			loc.Env = append(loc.Env,
				secretEnv("AWS_ACCESS_KEY_ID", creds, odoov1.S3AccessKeyIDKey),
				secretEnv("AWS_SECRET_ACCESS_KEY", creds, odoov1.S3SecretAccessKeyKey),
			)
		}
		loc.Env = append(loc.Env,
			rcloneEnv("TYPE", "s3"),
			rcloneSecretEnv("ACCESS_KEY_ID", creds, odoov1.S3AccessKeyIDKey),
			rcloneSecretEnv("SECRET_ACCESS_KEY", creds, odoov1.S3SecretAccessKeyKey),
		)
		if s3.Endpoint != "" {
			loc.Env = append(loc.Env, rcloneEnv("PROVIDER", "Other"), rcloneEnv("ENDPOINT", s3.Endpoint))
		} else {
			loc.Env = append(loc.Env, rcloneEnv("PROVIDER", "AWS"))
		}
		if s3.Region != "" {
			if !sl.rcloneS3 {
				loc.Env = append(loc.Env, plainEnv("AWS_DEFAULT_REGION", s3.Region))
				loc.ResticOptions = append(loc.ResticOptions, "s3.region="+s3.Region)
			}
			loc.Env = append(loc.Env, rcloneEnv("REGION", s3.Region))
		}
		if s3.ForcePathStyle {
			if !sl.rcloneS3 {
				loc.ResticOptions = append(loc.ResticOptions, "s3.bucket-lookup=path")
			}
			loc.Env = append(loc.Env, rcloneEnv("FORCE_PATH_STYLE", "true"))
		}
		bucketPath := strings.TrimSuffix(s3.Bucket+"/"+strings.TrimPrefix(layout.JoinStorePath(string(s3.Path), ""), "/"), "/")
		if sl.rcloneS3 {
			loc.Repository = "rclone:" + sl.remote + ":" + strings.TrimSuffix(
				s3.Bucket+"/"+strings.TrimPrefix(layout.JoinStorePath(string(s3.Path), repo), "/"), "/")
		} else {
			loc.Repository = S3Repository(s3, repo)
		}
		loc.ProbeTarget = sl.remote + ":" + bucketPath
	case spec.SFTP != nil:
		sftp := spec.SFTP
		port := sftp.Port
		if port == 0 {
			port = 22
		}
		loc.Env = append(loc.Env,
			rcloneEnv("TYPE", "sftp"),
			rcloneEnv("HOST", sftp.Host),
			rcloneEnv("PORT", strconv.Itoa(int(port))),
			rcloneEnv("USER", sftp.User),
			rcloneEnv("KEY_FILE", sl.sshMount+"/"+odoov1.SSHPrivateKeyKey),
			rcloneEnv("KNOWN_HOSTS_FILE", sl.sshMount+"/"+odoov1.SSHKnownHostsKey),
			// No remote commands: many SFTP-only servers refuse them.
			rcloneEnv("SHELL_TYPE", "none"),
			rcloneEnv("DISABLE_HASHCHECK", "true"),
		)
		loc.Volumes = append(loc.Volumes, corev1.Volume{
			Name: sl.volSSH,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName:  sftp.SSHSecret.Name,
				DefaultMode: ptr(int32(0o440)),
				// The private key is under either name, so a missing key
				// must not stop the Pod; the operator checks the Secret
				// before it creates a Job, and the agent reads the key that
				// is there.
				Items: []corev1.KeyToPath{
					{Key: odoov1.SSHPrivateKeyKey, Path: odoov1.SSHPrivateKeyKey},
					{Key: odoov1.SSHAuthPrivateKeyKey, Path: odoov1.SSHAuthPrivateKeyKey},
					{Key: odoov1.SSHKnownHostsKey, Path: odoov1.SSHKnownHostsKey},
				},
				Optional: ptr(true),
			}},
		})
		loc.Mounts = append(loc.Mounts, corev1.VolumeMount{Name: sl.volSSH, MountPath: sl.sshMount, ReadOnly: true})
		loc.Repository = "rclone:" + sl.remote + ":" + layout.JoinStorePath(string(sftp.Path), repo)
		loc.ProbeTarget = sl.remote + ":" + layout.JoinStorePath(string(sftp.Path), "")
	case spec.NFS != nil:
		loc.Volumes = append(loc.Volumes, corev1.Volume{
			Name:         sl.volStore,
			VolumeSource: corev1.VolumeSource{NFS: &corev1.NFSVolumeSource{Server: spec.NFS.Server, Path: spec.NFS.Path}},
		})
		loc.Mounts = append(loc.Mounts, corev1.VolumeMount{Name: sl.volStore, MountPath: sl.storeMount})
		loc.Repository = strings.TrimSuffix(sl.storeMount+"/"+repo, "/")
		loc.ProbeTarget = sl.storeMount
	case spec.PVC != nil:
		// The claim's root is mounted and the folders are created by restic:
		// a subPath mount would have the kubelet create them as root.
		loc.Volumes = append(loc.Volumes, corev1.Volume{
			Name: sl.volStore,
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: spec.PVC.ClaimName,
			}},
		})
		loc.Mounts = append(loc.Mounts, corev1.VolumeMount{Name: sl.volStore, MountPath: sl.storeMount})
		root := sl.storeMount
		if sub := layout.JoinStorePath(string(spec.PVC.SubPath), ""); sub != "" {
			root += "/" + strings.TrimPrefix(sub, "/")
		}
		loc.Repository = strings.TrimSuffix(root+"/"+repo, "/")
		loc.ProbeTarget = root
	default:
		return loc, fmt.Errorf("store %s sets none of s3, sftp, nfs, pvc", store.Name)
	}

	key := spec.EncryptionKeySecret.Key
	if key == "" {
		key = passwordFile
	}
	loc.Volumes = append(loc.Volumes, corev1.Volume{
		Name: sl.volPass,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName:  spec.EncryptionKeySecret.Name,
			DefaultMode: ptr(int32(0o440)),
			Items:       []corev1.KeyToPath{{Key: key, Path: passwordFile}},
		}},
	})
	loc.Mounts = append(loc.Mounts, corev1.VolumeMount{Name: sl.volPass, MountPath: sl.passMount, ReadOnly: true})
	loc.Env = append(loc.Env, plainEnv(sl.env+"RESTIC_PASSWORD_FILE", sl.passMount+"/"+passwordFile))
	if repo != "" {
		loc.Env = append(loc.Env, plainEnv(sl.env+"RESTIC_REPOSITORY", loc.Repository))
	}
	return loc, nil
}

// StoreSecrets are the Secrets (and keys) a store's Jobs read. A key written
// "a|b" is either of them.
func StoreSecrets(store *odoov1.OdooBackupStore) map[string][]string {
	out := map[string][]string{}
	add := func(name string, keys ...string) {
		if name != "" {
			out[name] = append(out[name], keys...)
		}
	}
	spec := &store.Spec
	switch {
	case spec.S3 != nil:
		add(spec.S3.CredentialsSecret.Name, odoov1.S3AccessKeyIDKey, odoov1.S3SecretAccessKeyKey)
	case spec.SFTP != nil:
		add(spec.SFTP.SSHSecret.Name, odoov1.SSHPrivateKeyKey+"|"+odoov1.SSHAuthPrivateKeyKey, odoov1.SSHKnownHostsKey)
	}
	key := spec.EncryptionKeySecret.Key
	if key == "" {
		key = passwordFile
	}
	add(spec.EncryptionKeySecret.Name, key)
	return out
}

func ptr[T any](v T) *T { return &v }

// shortHash is the first 8 hex digits of the sha256 of the parts.
func shortHash(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])[:8]
}

// jobName is "<prefix>-<kind>-<hash>-<attempt>", within 63 characters.
func jobName(prefix, kind string, attempt int32, hashParts ...string) string {
	suffix := fmt.Sprintf("-%s-%s-%d", kind, shortHash(hashParts...), attempt)
	if max := 63 - len(suffix); len(prefix) > max {
		prefix = strings.TrimRight(prefix[:max], "-.")
	}
	return prefix + suffix
}

// Params are what every instance Job needs.
type Params struct {
	// Image is the backup image.
	Image string
	// Instance is the OdooDeployment.
	Instance *odoov1.OdooDeployment
	// Database is the instance's database (status.database.name).
	Database string
	// Store is where the backup is written or read; nil for a cleanup.
	Store *odoov1.OdooBackupStore
	// OperatorVersion goes into the manifest.
	OperatorVersion string
	// Affinity pins the pod next to the running Odoo pod (a ReadWriteOnce
	// filestore volume can only be mounted on one node).
	Affinity *corev1.Affinity
	// Offline is set when Odoo is stopped for the backup, whatever
	// spec.backup.consistency says (a ReadWriteOncePod volume).
	Offline bool
	// Retention, when set, is applied by the backup Job after the backup.
	Retention *Retention
	// Copies are the stores a backup Job copies the new snapshot to.
	Copies []*odoov1.OdooBackupStore
}

// Retention is what a backup Job needs to thin the repository.
type Retention struct {
	Spec     odoov1.OdooBackupRetention
	TimeZone string
	// ProtectBackups are OdooBackups marked keep.
	ProtectBackups []string
	// ProtectSnapshots are snapshot IDs a pending restore needs.
	ProtectSnapshots []string
}

func (r *Retention) args() []string {
	c := r.Spec.Counts()
	args := []string{"--retention", "--time-zone", r.TimeZone}
	for _, k := range []struct {
		flag string
		n    int32
	}{
		{"--keep-last", c.Last}, {"--keep-hourly", c.Hourly}, {"--keep-daily", c.Daily},
		{"--keep-weekly", c.Weekly}, {"--keep-monthly", c.Monthly}, {"--keep-yearly", c.Yearly},
		{"--keep-manual", c.Manual}, {"--keep-pre-upgrade", c.PreUpgrade},
	} {
		if k.n > 0 {
			args = append(args, k.flag, strconv.Itoa(int(k.n)))
		}
	}
	if r.Spec.Within != "" {
		args = append(args, "--keep-within", r.Spec.Within)
	}
	for _, b := range r.ProtectBackups {
		args = append(args, "--protect-backup", b)
	}
	for _, id := range r.ProtectSnapshots {
		args = append(args, "--protect-snapshot", id)
	}
	return args
}

func (p Params) labels(kind string) map[string]string {
	return map[string]string{
		odoov1.LabelOdooDeployment: p.Instance.Name,
		odoov1.LabelJobKind:        kind,
	}
}

func baseEnv() []corev1.EnvVar {
	return []corev1.EnvVar{
		plainEnv("HOME", tmpDir),
		plainEnv("TMPDIR", tmpDir),
		plainEnv("RESTIC_CACHE_DIR", tmpDir+"/restic-cache"),
	}
}

func (p Params) commonArgs(loc Location) []string {
	args := make([]string, 0, 2+2*len(loc.ResticOptions))
	args = append(args, "--maintenance-db", p.Instance.Spec.Database.MaintenanceDatabaseValue())
	for _, o := range loc.ResticOptions {
		args = append(args, "--restic-option", o)
	}
	return args
}

func (p Params) resources() corev1.ResourceRequirements {
	if r := p.Instance.Spec.Backup.Resources; r != nil {
		return *r.DeepCopy()
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		},
	}
}

func (p Params) ttl() *int32 {
	if t := p.Instance.Spec.Jobs.TTLSecondsAfterFinished; t != nil {
		return ptr(*t)
	}
	return ptr(defaultTTL)
}

func (p Params) podSpec(command []string, env []corev1.EnvVar, volumes []corev1.Volume, mounts []corev1.VolumeMount) corev1.PodSpec {
	return corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:                     ContainerName,
			Image:                    p.Image,
			ImagePullPolicy:          corev1.PullIfNotPresent,
			Command:                  command,
			Env:                      env,
			VolumeMounts:             mounts,
			Resources:                p.resources(),
			TerminationMessagePath:   layout.TerminationLog,
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		}},
		Volumes:                      volumes,
		SecurityContext:              p.Instance.PodSecurityContextValue(),
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: ptr(false),
		Affinity:                     p.Affinity,
	}
}

func (p Params) job(name, kind string, extraLabels map[string]string, spec corev1.PodSpec, backoff int32, deadline int64) batchv1.Job {
	labels := p.labels(kind)
	for k, v := range extraLabels {
		labels[k] = v
	}
	return batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Instance.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       spec,
			},
			Parallelism:             ptr(int32(1)),
			Completions:             ptr(int32(1)),
			BackoffLimit:            ptr(backoff),
			ActiveDeadlineSeconds:   ptr(deadline),
			TTLSecondsAfterFinished: p.ttl(),
		},
	}
}

func (p Params) dataVolume() corev1.Volume {
	return corev1.Volume{
		Name: volData,
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
			ClaimName: p.Instance.GetPvcName(),
		}},
	}
}

func emptyDir(name string) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
}

// BackupJobName is the name of attempt of a backup's Job.
func BackupJobName(instance *odoov1.OdooDeployment, backup string, attempt int32) string {
	return jobName(instance.Name, KindBackup, attempt, instance.Namespace, backup)
}

// BackupJob renders the Job that takes backup. waitForCleanup defers it
// while Odoo's filestore cleanup runs.
func BackupJob(p Params, backup *odoov1.OdooBackup, kind string, attempt int32, waitForCleanup bool) (batchv1.Job, error) {
	loc, err := Locate(p.Store, layout.Repository(p.Instance.Namespace, p.Instance.Name))
	if err != nil {
		return batchv1.Job{}, err
	}
	od := p.Instance
	consistency := od.Spec.Backup.ConsistencyValue()
	if p.Offline {
		consistency = odoov1.BackupConsistencyOffline
	}
	command := []string{toolBinary, "backup",
		"--namespace", od.Namespace,
		"--deployment", od.Name,
		"--backup", backup.Name,
		"--kind", kind,
		"--image", od.Status.AppliedImage,
		"--upgrade-token", od.Status.AppliedUpgradeToken,
		"--consistency", string(consistency),
		"--root", layout.BackupRoot,
	}
	if waitForCleanup {
		command = append(command, "--wait-for-cleanup")
	}
	if p.Retention != nil {
		command = append(command, p.Retention.args()...)
	}
	command = append(command, p.commonArgs(loc)...)
	volumes := append([]corev1.Volume{
		od.ConfigSecretVolume(volConfig),
		p.dataVolume(),
		emptyDir(volScratch),
		emptyDir(volTmp),
	}, loc.Volumes...)
	mounts := append([]corev1.VolumeMount{
		{Name: volConfig, MountPath: layout.ConfigMount, ReadOnly: true},
		{Name: volScratch, MountPath: layout.BackupRoot},
		// The filestore folder only, read-only: a backup never writes there.
		{Name: volData, MountPath: layout.BackupRoot + "/" + layout.FilestoreDir,
			SubPath: layout.FilestorePath(p.Database), ReadOnly: true},
		{Name: volTmp, MountPath: tmpDir},
	}, loc.Mounts...)
	env := append(baseEnv(), loc.Env...)
	for i, store := range p.Copies {
		copyLoc, err := LocateCopy(store, layout.Repository(od.Namespace, od.Name), i+1)
		if err != nil {
			return batchv1.Job{}, err
		}
		command = append(command, "--copy", store.Name)
		env = append(env, copyLoc.Env...)
		volumes = append(volumes, copyLoc.Volumes...)
		mounts = append(mounts, copyLoc.Mounts...)
	}
	spec := p.podSpec(command, env, volumes, mounts)
	// The kubelet would create a missing subPath folder as root; the init
	// container creates it as Odoo's user first.
	spec.InitContainers = []corev1.Container{{
		Name:            "prepare",
		Image:           p.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{toolBinary, "prepare", "--data", layout.DataMount, "--database", p.Database},
		VolumeMounts:    []corev1.VolumeMount{{Name: volData, MountPath: layout.DataMount}},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
		},
	}}
	// Odoo is down for an offline backup: a failure ends it at once, so the
	// operator brings Odoo back instead of waiting for a second attempt.
	backoff := backupBackoff
	if p.Offline {
		backoff = 0
	}
	return p.job(BackupJobName(od, backup.Name, attempt), KindBackup, map[string]string{LabelBackup: backup.Name},
		spec, backoff, od.Spec.Backup.ActiveDeadlineSecondsValue()), nil
}

// RestoreJobName is the name of attempt of a restore's Job.
func RestoreJobName(instance *odoov1.OdooDeployment, restore string, attempt int32) string {
	return jobName(instance.Name, KindRestore, attempt, instance.Namespace, restore)
}

// RestoreJob renders the Job that restores snapshot in place (or, with
// undoSuffix, undoes an earlier restore; the store is then not needed).
func RestoreJob(p Params, restore *odoov1.OdooRestore, suffix, snapshot, undoSuffix string,
	expectModules []string, attempt int32) (batchv1.Job, error) {
	od := p.Instance
	command := []string{toolBinary, "restore", "--snapshot", snapshot}
	if undoSuffix != "" {
		command = []string{toolBinary, "undo", "--undo-suffix", undoSuffix}
	}
	command = append(command,
		"--suffix", suffix,
		"--data", layout.DataMount,
		"--expect-modules", strings.Join(expectModules, ","),
	)
	env := baseEnv()
	volumes := []corev1.Volume{od.ConfigSecretVolume(volConfig), p.dataVolume(), emptyDir(volTmp)}
	mounts := []corev1.VolumeMount{
		{Name: volConfig, MountPath: layout.ConfigMount, ReadOnly: true},
		{Name: volData, MountPath: layout.DataMount},
		{Name: volTmp, MountPath: tmpDir},
	}
	var loc Location
	if undoSuffix == "" {
		var err error
		loc, err = Locate(p.Store, layout.Repository(od.Namespace, od.Name))
		if err != nil {
			return batchv1.Job{}, err
		}
		env = append(env, loc.Env...)
		volumes = append(volumes, loc.Volumes...)
		mounts = append(mounts, loc.Mounts...)
	}
	command = append(command, p.commonArgs(loc)...)
	spec := p.podSpec(command, env, volumes, mounts)
	return p.job(RestoreJobName(od, restore.Name, attempt), KindRestore, map[string]string{LabelRestore: restore.Name},
		spec, restoreBackoff, od.Spec.Backup.ActiveDeadlineSecondsValue()), nil
}

// DrillJobName is the Job of the drill with suffix.
func DrillJobName(instance *odoov1.OdooDeployment, suffix string) string {
	return jobName(instance.Name, KindDrill, 0, instance.Namespace, suffix)
}

// Drill is one restore drill.
type Drill struct {
	// Suffix names the scratch database.
	Suffix string
	// Snapshot to restore from p.Store; Backup is its OdooBackup.
	Snapshot string
	Backup   string
	// OdooImage loads the restored database.
	OdooImage string
	// ExpectModules are the modules the instance should have.
	ExpectModules []string
}

// drillScript runs "odoo shell" on the restored database when the load
// succeeded: Odoo loads its registry, reads records of core models through
// the ORM and reads attachments from the scratch filestore, which must give
// every byte of the file (a missing file the load step already judged). It
// records how that ended, exiting 0 either way so the last step always drops the scratch
// database. The file names are the agent's (agent.DrillLoadedFile and the
// rest).
const drillScript = `[ -f "$DRILL_ROOT/loaded" ] || exit 0
"$@" >"$DRILL_ROOT/odoo.log" 2>&1 <<'PY'
import os
for model in ("res.company", "res.users", "res.partner"):
    env[model].sudo().with_context(active_test=False).search([], limit=1).read()
atts = env["ir.attachment"].sudo().search(
    [("store_fname", "!=", False), "|", ("res_field", "=", False), ("res_field", "!=", False)], limit=20)
paths = {a: a._full_path(a.store_fname) for a in atts}
bad = [a.store_fname for a, p in paths.items() if os.path.isfile(p) and len(a.raw or b"") != os.path.getsize(p)]
if bad:
    raise SystemExit("Odoo cannot read %d attachment(s), e.g. %s" % (len(bad), ", ".join(bad[:3])))
print("drill: Odoo read core records and %d attachments" % len(atts))
PY
rc=$?
cat "$DRILL_ROOT/odoo.log"
echo "$rc" >"$DRILL_ROOT/odoo.exit"
exit 0`

// DrillJob renders the Job of a restore drill: the backup agent restores
// the snapshot into a scratch database and an emptyDir and checks it, the
// backup's Odoo image reads it (odoo shell), and the agent drops
// the scratch database and reports. It never mounts the data volume.
func DrillJob(p Params, d Drill) (batchv1.Job, error) {
	od := p.Instance
	loc, err := Locate(p.Store, layout.Repository(od.Namespace, od.Name))
	if err != nil {
		return batchv1.Job{}, err
	}
	scratch := layout.DrillName(p.Database, d.Suffix)
	common := append([]string{"--suffix", d.Suffix, "--root", layout.DrillRoot}, p.commonArgs(loc)...)
	load := append([]string{toolBinary, "drill-load", "--snapshot", d.Snapshot,
		"--expect-modules", strings.Join(d.ExpectModules, ",")}, common...)
	finish := append([]string{toolBinary, "drill-finish"}, common...)

	scratchVol := emptyDir(volDrill)
	if limit := od.Spec.Backup.Drill.ScratchSizeLimit; limit != nil {
		scratchVol.EmptyDir.SizeLimit = ptr(limit.DeepCopy())
	}
	volumes := append([]corev1.Volume{od.ConfigSecretVolume(volConfig), scratchVol, emptyDir(volTmp)}, loc.Volumes...)
	agentMounts := []corev1.VolumeMount{
		{Name: volConfig, MountPath: layout.ConfigMount, ReadOnly: true},
		{Name: volDrill, MountPath: layout.DrillRoot},
		{Name: volTmp, MountPath: tmpDir},
	}
	spec := p.podSpec(finish, baseEnv(), volumes, agentMounts)
	loader := spec.Containers[0]
	loader.Name = "load"
	loader.Command = load
	loader.Env = append(baseEnv(), loc.Env...)
	loader.VolumeMounts = append(append([]corev1.VolumeMount(nil), agentMounts...), loc.Mounts...)
	loader.TerminationMessagePath = ""
	loader.TerminationMessagePolicy = ""

	odooCmd := make([]string, 0, 13+len(od.Spec.OdooCommand))
	odooCmd = append(odooCmd, "sh", "-c", drillScript, "sh")
	odooCmd = append(odooCmd, od.Spec.OdooCommand...)
	odooCmd = append(odooCmd, "shell", "-c", odoov1.OdooConfigFile, "--no-http",
		"--max-cron-threads=0", "-d", scratch, "--data-dir", layout.DrillRoot)
	odoo := corev1.Container{
		Name:            "odoo",
		Image:           d.OdooImage,
		ImagePullPolicy: od.ImagePullPolicyValue(),
		Command:         odooCmd,
		Env:             append(append([]corev1.EnvVar(nil), od.Spec.Env...), plainEnv("DRILL_ROOT", layout.DrillRoot)),
		EnvFrom:         od.Spec.EnvFrom,
		Resources:       *od.Spec.Resources.DeepCopy(),
		VolumeMounts: []corev1.VolumeMount{
			{Name: volConfig, MountPath: odoov1.OdooConfigMountPath, ReadOnly: true},
			{Name: volDrill, MountPath: layout.DrillRoot},
		},
	}
	spec.InitContainers = []corev1.Container{loader, odoo}
	spec.ImagePullSecrets = od.Spec.ImagePullSecrets
	return p.job(DrillJobName(od, d.Suffix), KindDrill, map[string]string{LabelBackup: d.Backup},
		spec, drillBackoff, od.Spec.Backup.ActiveDeadlineSecondsValue()), nil
}

// CleanupJobName is the name of attempt of a restore's cleanup Job.
func CleanupJobName(instance *odoov1.OdooDeployment, restore string, attempt int32) string {
	return jobName(instance.Name, KindCleanup, attempt, instance.Namespace, restore)
}

// CleanupJob renders the Job that deletes what restore left behind.
func CleanupJob(p Params, restore *odoov1.OdooRestore, suffix string, attempt int32) batchv1.Job {
	od := p.Instance
	command := append([]string{toolBinary, "cleanup", "--suffix", suffix, "--data", layout.DataMount}, p.commonArgs(Location{})...)
	volumes := []corev1.Volume{od.ConfigSecretVolume(volConfig), p.dataVolume(), emptyDir(volTmp)}
	mounts := []corev1.VolumeMount{
		{Name: volConfig, MountPath: layout.ConfigMount, ReadOnly: true},
		{Name: volData, MountPath: layout.DataMount},
		{Name: volTmp, MountPath: tmpDir},
	}
	spec := p.podSpec(command, baseEnv(), volumes, mounts)
	return p.job(CleanupJobName(od, restore.Name, attempt), KindCleanup, map[string]string{LabelRestore: restore.Name},
		spec, cleanupBackoff, cleanupDeadline)
}

// ProbeJobName is the probe Job of a store for a spec/Secret hash.
func ProbeJobName(store *odoov1.OdooBackupStore, hash string) string {
	return jobName(store.Name, "probe", 0, store.Namespace, hash)
}

// ProbeJob renders the Job that checks a store can be written, read and
// deleted from.
func ProbeJob(image string, store *odoov1.OdooBackupStore, hash string) (batchv1.Job, error) {
	loc, err := Locate(store, "")
	if err != nil {
		return batchv1.Job{}, err
	}
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("32Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
	}
	return storeJob(image, store, ProbeJobName(store, hash), KindProbe, loc,
		[]string{toolBinary, "probe", "--target", loc.ProbeTarget}, resources, probeDeadline, 3600), nil
}

// ReachJobName is the Job that checks the store before an offline backup
// stops Odoo. The backup's uid tells a backup taken again under the same
// name from the earlier one.
func ReachJobName(instance *odoov1.OdooDeployment, backup *odoov1.OdooBackup) string {
	return jobName(instance.Name, "reach", 0, instance.Namespace, backup.Name, string(backup.UID))
}

// ReachJob renders the Job that checks, while Odoo still serves, that the
// store an offline backup writes to can be written, read and deleted from.
// It runs the store probe, which gives up within minutes where restic would
// retry an unreachable store for a quarter of an hour.
func ReachJob(image string, store *odoov1.OdooBackupStore, instance *odoov1.OdooDeployment, backup *odoov1.OdooBackup) (batchv1.Job, error) {
	job, err := ProbeJob(image, store, "")
	if err != nil {
		return batchv1.Job{}, err
	}
	job.Name = ReachJobName(instance, backup)
	job.Spec.TTLSecondsAfterFinished = ptr(reachTTL)
	for _, labels := range []map[string]string{job.Labels, job.Spec.Template.Labels} {
		labels[odoov1.LabelJobKind] = KindReach
		labels[LabelBackup] = backup.Name
	}
	return job, nil
}

// StoreTaskJobName is the Job of a store's check or prune for one
// scheduled time.
func StoreTaskJobName(store *odoov1.OdooBackupStore, kind string, scheduled string) string {
	return jobName(store.Name, strings.TrimPrefix(kind, "store-"), 0, store.Namespace, scheduled)
}

// StoreTask is a check or prune over some of a store's repositories.
type StoreTask struct {
	// Kind is KindCheck or KindPrune.
	Kind string
	// Scheduled identifies the scheduled time the task runs for.
	Scheduled string
	// Repositories are instance folders (layout.Repository) in the store.
	Repositories []string
	// Part is the part of the data a check reads ("3/7"), "all", or "" for
	// the structure only.
	Part string
}

// StoreTaskJob renders the Job that checks or prunes a store's repositories.
func StoreTaskJob(image string, store *odoov1.OdooBackupStore, task StoreTask) (batchv1.Job, error) {
	loc, err := Locate(store, "")
	if err != nil {
		return batchv1.Job{}, err
	}
	command := []string{toolBinary, strings.TrimPrefix(task.Kind, "store-")}
	if task.Kind == KindCheck && task.Part != "" {
		command = append(command, "--read-data", task.Part)
	}
	for _, o := range loc.ResticOptions {
		command = append(command, "--restic-option", o)
	}
	for _, repo := range task.Repositories {
		repoLoc, err := Locate(store, repo)
		if err != nil {
			return batchv1.Job{}, err
		}
		command = append(command, "--repository", repo+"="+repoLoc.Repository)
	}
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
	}
	return storeJob(image, store, StoreTaskJobName(store, task.Kind, task.Scheduled), task.Kind, loc,
		command, resources, storeTaskDeadline, defaultTTL), nil
}

func storeJob(image string, store *odoov1.OdooBackupStore, name, kind string, loc Location, command []string,
	resources corev1.ResourceRequirements, deadline int64, ttl int32) batchv1.Job {
	labels := map[string]string{odoov1.LabelJobKind: kind, LabelStore: store.Name}
	if store.Spec.Resources != nil {
		resources = *store.Spec.Resources.DeepCopy()
	}
	return batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: store.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:                     ContainerName,
						Image:                    image,
						ImagePullPolicy:          corev1.PullIfNotPresent,
						Command:                  command,
						Env:                      append(baseEnv(), loc.Env...),
						VolumeMounts:             append([]corev1.VolumeMount{{Name: volTmp, MountPath: tmpDir}}, loc.Mounts...),
						Resources:                resources,
						TerminationMessagePath:   layout.TerminationLog,
						TerminationMessagePolicy: corev1.TerminationMessageReadFile,
					}},
					Volumes: append([]corev1.Volume{emptyDir(volTmp)}, loc.Volumes...),
					// The uid the Odoo pods run as by default, so a local store
					// that works for the probe works for the backups.
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:    ptr(int64(100)),
						RunAsGroup:   ptr(int64(101)),
						RunAsNonRoot: ptr(true),
						FSGroup:      ptr(int64(101)),
					},
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: ptr(false),
				},
			},
			BackoffLimit:            ptr(int32(0)),
			ActiveDeadlineSeconds:   ptr(deadline),
			TTLSecondsAfterFinished: ptr(ttl),
		},
	}
}
