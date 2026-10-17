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
	volTmp          = "tmp"
	volStore        = "store"
	volStorePass    = "store-password"
	volStoreSSH     = "store-ssh"
	rcloneRemote    = "store"
	passwordFile    = "password"
	sshMount        = layout.SecretsMount + "/ssh"
	passwordMount   = layout.SecretsMount + "/restic"
	pgBinTemplate   = "/usr/libexec/postgresql%d"
	toolBinary      = "odoo-backup"
	defaultTTL      = int32(86400)
	backupBackoff   = int32(1)
	restoreBackoff  = int32(2)
	cleanupBackoff  = int32(2)
	probeDeadline   = int64(300)
	cleanupDeadline = int64(3600)
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

func rcloneEnv(option, value string) corev1.EnvVar {
	return plainEnv("RCLONE_CONFIG_"+strings.ToUpper(rcloneRemote)+"_"+option, value)
}

// S3Repository is the restic repository URL of repo inside an S3 store.
func S3Repository(s3 *odoov1.S3Store, repo string) string {
	endpoint := strings.TrimSuffix(s3.Endpoint, "/")
	if endpoint == "" {
		endpoint = "s3.amazonaws.com"
	}
	p := layout.JoinStorePath(string(s3.Path), repo)
	url := "s3:" + endpoint + "/" + s3.Bucket
	if p != "" {
		url += "/" + p
	}
	return url
}

// Locate returns how a Job reaches store; repo is the instance's folder
// (layout.Repository), or "" for a probe of the store root.
func Locate(store *odoov1.OdooBackupStore, repo string) (Location, error) {
	spec := &store.Spec
	loc := Location{}
	switch {
	case spec.S3 != nil:
		s3 := spec.S3
		creds := s3.CredentialsSecret.Name
		loc.Env = append(loc.Env,
			secretEnv("AWS_ACCESS_KEY_ID", creds, odoov1.S3AccessKeyIDKey),
			secretEnv("AWS_SECRET_ACCESS_KEY", creds, odoov1.S3SecretAccessKeyKey),
			rcloneEnv("TYPE", "s3"),
			secretEnv("RCLONE_CONFIG_STORE_ACCESS_KEY_ID", creds, odoov1.S3AccessKeyIDKey),
			secretEnv("RCLONE_CONFIG_STORE_SECRET_ACCESS_KEY", creds, odoov1.S3SecretAccessKeyKey),
		)
		if s3.Endpoint != "" {
			loc.Env = append(loc.Env, rcloneEnv("PROVIDER", "Other"), rcloneEnv("ENDPOINT", s3.Endpoint))
		} else {
			loc.Env = append(loc.Env, rcloneEnv("PROVIDER", "AWS"))
		}
		if s3.Region != "" {
			loc.Env = append(loc.Env, plainEnv("AWS_DEFAULT_REGION", s3.Region), rcloneEnv("REGION", s3.Region))
			loc.ResticOptions = append(loc.ResticOptions, "s3.region="+s3.Region)
		}
		if s3.ForcePathStyle {
			loc.ResticOptions = append(loc.ResticOptions, "s3.bucket-lookup=path")
			loc.Env = append(loc.Env, rcloneEnv("FORCE_PATH_STYLE", "true"))
		}
		loc.Repository = S3Repository(s3, repo)
		loc.ProbeTarget = rcloneRemote + ":" + strings.TrimSuffix(s3.Bucket+"/"+layout.JoinStorePath(string(s3.Path), ""), "/")
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
			rcloneEnv("KEY_FILE", sshMount+"/"+odoov1.SSHPrivateKeyKey),
			rcloneEnv("KNOWN_HOSTS_FILE", sshMount+"/"+odoov1.SSHKnownHostsKey),
			// No remote commands: many SFTP-only servers refuse them.
			rcloneEnv("SHELL_TYPE", "none"),
			rcloneEnv("DISABLE_HASHCHECK", "true"),
		)
		loc.Volumes = append(loc.Volumes, corev1.Volume{
			Name: volStoreSSH,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName:  sftp.SSHSecret.Name,
				DefaultMode: ptr(int32(0o440)),
				Items: []corev1.KeyToPath{
					{Key: odoov1.SSHPrivateKeyKey, Path: odoov1.SSHPrivateKeyKey},
					{Key: odoov1.SSHKnownHostsKey, Path: odoov1.SSHKnownHostsKey},
				},
			}},
		})
		loc.Mounts = append(loc.Mounts, corev1.VolumeMount{Name: volStoreSSH, MountPath: sshMount, ReadOnly: true})
		loc.Repository = "rclone:" + rcloneRemote + ":" + layout.JoinStorePath(string(sftp.Path), repo)
		loc.ProbeTarget = rcloneRemote + ":" + layout.JoinStorePath(string(sftp.Path), "")
	case spec.NFS != nil:
		loc.Volumes = append(loc.Volumes, corev1.Volume{
			Name:         volStore,
			VolumeSource: corev1.VolumeSource{NFS: &corev1.NFSVolumeSource{Server: spec.NFS.Server, Path: spec.NFS.Path}},
		})
		loc.Mounts = append(loc.Mounts, corev1.VolumeMount{Name: volStore, MountPath: layout.StoreMount})
		loc.Repository = strings.TrimSuffix(layout.StoreMount+"/"+repo, "/")
		loc.ProbeTarget = layout.StoreMount
	case spec.PVC != nil:
		// The claim's root is mounted and the folders are created by restic:
		// a subPath mount would have the kubelet create them as root.
		loc.Volumes = append(loc.Volumes, corev1.Volume{
			Name: volStore,
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: spec.PVC.ClaimName,
			}},
		})
		loc.Mounts = append(loc.Mounts, corev1.VolumeMount{Name: volStore, MountPath: layout.StoreMount})
		root := layout.StoreMount
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
		Name: volStorePass,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName:  spec.EncryptionKeySecret.Name,
			DefaultMode: ptr(int32(0o440)),
			Items:       []corev1.KeyToPath{{Key: key, Path: passwordFile}},
		}},
	})
	loc.Mounts = append(loc.Mounts, corev1.VolumeMount{Name: volStorePass, MountPath: passwordMount, ReadOnly: true})
	loc.Env = append(loc.Env, plainEnv("RESTIC_PASSWORD_FILE", passwordMount+"/"+passwordFile))
	if repo != "" {
		loc.Env = append(loc.Env, plainEnv("RESTIC_REPOSITORY", loc.Repository))
	}
	return loc, nil
}

// StoreSecrets are the Secrets (and keys) a store's Jobs read.
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
		add(spec.SFTP.SSHSecret.Name, odoov1.SSHPrivateKeyKey, odoov1.SSHKnownHostsKey)
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
}

func (p Params) labels(kind string) map[string]string {
	return map[string]string{
		odoov1.LabelOdooDeployment: p.Instance.Name,
		odoov1.LabelJobKind:        kind,
	}
}

func baseEnv() []corev1.EnvVar {
	return []corev1.EnvVar{
		plainEnv("HOME", "/tmp"),
		plainEnv("TMPDIR", "/tmp"),
		plainEnv("RESTIC_CACHE_DIR", "/tmp/restic-cache"),
		plainEnv("ODOO_BACKUP_PG_BIN_TEMPLATE", pgBinTemplate),
	}
}

func (p Params) commonArgs(loc Location) []string {
	args := []string{"--maintenance-db", p.Instance.Spec.Database.MaintenanceDatabaseValue()}
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
	command := []string{toolBinary, "backup",
		"--namespace", od.Namespace,
		"--deployment", od.Name,
		"--backup", backup.Name,
		"--kind", kind,
		"--image", od.Status.AppliedImage,
		"--upgrade-token", od.Status.AppliedUpgradeToken,
		"--consistency", string(od.Spec.Backup.ConsistencyValue()),
		"--root", layout.BackupRoot,
	}
	if waitForCleanup {
		command = append(command, "--wait-for-cleanup")
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
		{Name: volTmp, MountPath: "/tmp"},
	}, loc.Mounts...)
	spec := p.podSpec(command, append(baseEnv(), loc.Env...), volumes, mounts)
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
	return p.job(BackupJobName(od, backup.Name, attempt), KindBackup, map[string]string{LabelBackup: backup.Name},
		spec, backupBackoff, od.Spec.Backup.ActiveDeadlineSecondsValue()), nil
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
		{Name: volTmp, MountPath: "/tmp"},
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
		{Name: volTmp, MountPath: "/tmp"},
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
	labels := map[string]string{odoov1.LabelJobKind: KindProbe, LabelStore: store.Name}
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("32Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
	}
	if store.Spec.Resources != nil {
		resources = *store.Spec.Resources.DeepCopy()
	}
	return batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: ProbeJobName(store, hash), Namespace: store.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:                     ContainerName,
						Image:                    image,
						ImagePullPolicy:          corev1.PullIfNotPresent,
						Command:                  []string{toolBinary, "probe", "--target", loc.ProbeTarget},
						Env:                      append(baseEnv(), loc.Env...),
						VolumeMounts:             append([]corev1.VolumeMount{{Name: volTmp, MountPath: "/tmp"}}, loc.Mounts...),
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
			ActiveDeadlineSeconds:   ptr(probeDeadline),
			TTLSecondsAfterFinished: ptr(int32(3600)),
		},
	}, nil
}
