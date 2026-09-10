package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/MohanadAbugharbia/odoo-operator/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// OdooConfigMountPath is where the rendered odoo.conf is mounted inside the container.
	OdooConfigMountPath = "/opt/odoo"
	// OdooConfigFile is the full path of the rendered odoo.conf inside the container.
	OdooConfigFile = OdooConfigMountPath + "/" + odooConfigKey
	// odooConfigKey is the Secret key (and file name) of the rendered configuration.
	odooConfigKey = "odoo.conf"
	// dataVolumeName is the pod volume backed by the filestore PVC.
	dataVolumeName = "odoo-data"
	// httpPortName is the name of the container/Service port serving HTTP.
	httpPortName = "http"

	// Defaults that mirror the CRD defaults, applied when a pointer field is
	// nil (objects built in Go never pass through API-server defaulting).
	DefaultWorkers                    int32 = 2
	DefaultMaxCronThreads             int32 = 1
	DefaultReplicas                   int32 = 1
	DefaultJobActiveDeadlineSeconds   int64 = 3600
	DefaultJobTTLSecondsAfterFinished int32 = 86400
	DefaultJobBackoffLimit            int32 = 2
)

// ---- pointer accessors -------------------------------------------------------

// WorkersValue returns spec.config.workers or the CRD default.
func (o *OdooConfig) WorkersValue() int32 {
	if o.Workers == nil {
		return DefaultWorkers
	}
	return *o.Workers
}

// MaxCronThreadsValue returns spec.config.maxCronThreads or the CRD default.
func (o *OdooConfig) MaxCronThreadsValue() int32 {
	if o.MaxCronThreads == nil {
		return DefaultMaxCronThreads
	}
	return *o.MaxCronThreads
}

// WithoutDemoValue returns spec.config.withoutDemo or the CRD default (true).
func (o *OdooConfig) WithoutDemoValue() bool {
	if o.WithoutDemo == nil {
		return true
	}
	return *o.WithoutDemo
}

// ProxyModeValue returns spec.config.proxyMode or the CRD default (true).
func (o *OdooConfig) ProxyModeValue() bool {
	if o.ProxyMode == nil {
		return true
	}
	return *o.ProxyMode
}

// ListDbValue returns spec.config.listDb or the CRD default (false).
func (o *OdooConfig) ListDbValue() bool {
	if o.ListDb == nil {
		return false
	}
	return *o.ListDb
}

// ReplicasValue returns spec.replicas or the CRD default (1).
func (o *OdooDeploymentSpec) ReplicasValue() int32 {
	if o.Replicas == nil {
		return DefaultReplicas
	}
	return *o.Replicas
}

// OnImageChangeValue returns spec.upgrade.onImageChange or the CRD default (true).
func (o *OdooUpgradeConfig) OnImageChangeValue() bool {
	if o.OnImageChange == nil {
		return true
	}
	return *o.OnImageChange
}

// ActiveDeadlineSecondsValue returns spec.jobs.activeDeadlineSeconds or the CRD default.
func (o *OdooJobConfig) ActiveDeadlineSecondsValue() int64 {
	if o.ActiveDeadlineSeconds == nil {
		return DefaultJobActiveDeadlineSeconds
	}
	return *o.ActiveDeadlineSeconds
}

// TTLSecondsAfterFinishedValue returns spec.jobs.ttlSecondsAfterFinished or the CRD default.
func (o *OdooJobConfig) TTLSecondsAfterFinishedValue() int32 {
	if o.TTLSecondsAfterFinished == nil {
		return DefaultJobTTLSecondsAfterFinished
	}
	return *o.TTLSecondsAfterFinished
}

// BackoffLimitValue returns spec.jobs.backoffLimit or the CRD default.
func (o *OdooJobConfig) BackoffLimitValue() int32 {
	if o.BackoffLimit == nil {
		return DefaultJobBackoffLimit
	}
	return *o.BackoffLimit
}

// EnabledValue returns spec.probes.enabled or the CRD default (true).
func (o *OdooProbesConfig) EnabledValue() bool {
	if o.Enabled == nil {
		return true
	}
	return *o.Enabled
}

// CreatePolicyValue returns spec.database.createPolicy or the CRD default.
func (o *OdooDatabaseConfig) CreatePolicyValue() DatabaseCreatePolicy {
	if o.CreatePolicy == "" {
		return DatabaseCreatePolicyIfNotExists
	}
	return o.CreatePolicy
}

// DeletionPolicyValue returns spec.database.deletionPolicy or the CRD default.
func (o *OdooDatabaseConfig) DeletionPolicyValue() DeletionPolicy {
	if o.DeletionPolicy == "" {
		return DeletionPolicyRetain
	}
	return o.DeletionPolicy
}

// MaintenanceDatabaseValue returns spec.database.maintenanceDatabase or the CRD default.
func (o *OdooDatabaseConfig) MaintenanceDatabaseValue() string {
	if o.MaintenanceDatabase == "" {
		return "postgres"
	}
	return o.MaintenanceDatabase
}

// DeletionPolicyValue returns spec.odooFilestore.deletionPolicy or the CRD default.
func (o *PersistentVolumeClaimSpec) DeletionPolicyValue() DeletionPolicy {
	if o.DeletionPolicy == "" {
		return DeletionPolicyRetain
	}
	return o.DeletionPolicy
}

// ---- database connection details --------------------------------------------

func (odooDbConfig *OdooDatabaseConfig) GetHost(client client.Client, ctx context.Context, namespace string) (string, error) {
	// Use the HostFromSecret if it is provided
	if odooDbConfig.HostFromSecret.Name != "" && odooDbConfig.HostFromSecret.Key != "" {
		host, err := utils.GetSecretValue(client, ctx, namespace, odooDbConfig.HostFromSecret.Name, odooDbConfig.HostFromSecret.Key)
		return host, err
	}
	// If HostFromSecret is not provided, use the default host, which can also be given by the user
	return strings.TrimSpace(odooDbConfig.Host), nil
}

func (odooDbConfig *OdooDatabaseConfig) GetPort(client client.Client, ctx context.Context, namespace string) (int32, error) {
	if odooDbConfig.PortFromSecret.Name != "" && odooDbConfig.PortFromSecret.Key != "" {
		port, err := utils.GetInt32SecretValue(client, ctx, namespace, odooDbConfig.PortFromSecret.Name, odooDbConfig.PortFromSecret.Key)
		return port, err
	}
	// If port is not provided, use the OdooDatabaseConfig Port
	return odooDbConfig.Port, nil
}

func (odooDbConfig *OdooDatabaseConfig) GetUser(client client.Client, ctx context.Context, namespace string) (string, error) {
	// Use the UserFromSecret if it is provided
	if odooDbConfig.UserFromSecret.Name != "" && odooDbConfig.UserFromSecret.Key != "" {
		user, err := utils.GetSecretValue(client, ctx, namespace, odooDbConfig.UserFromSecret.Name, odooDbConfig.UserFromSecret.Key)
		return user, err
	}
	// If UserFromSecret is not provided, use the default user, which can also be given by the user
	return strings.TrimSpace(odooDbConfig.User), nil
}
func (odooDbConfig *OdooDatabaseConfig) GetPassword(client client.Client, ctx context.Context, namespace string) (string, error) {
	// Use the PasswordFromSecret if it is provided
	if odooDbConfig.PasswordFromSecret.Name != "" && odooDbConfig.PasswordFromSecret.Key != "" {
		password, err := utils.GetSecretValue(client, ctx, namespace, odooDbConfig.PasswordFromSecret.Name, odooDbConfig.PasswordFromSecret.Key)
		return password, err
	}
	// If PasswordFromSecret is not provided, return an error
	return "", utils.ErrSecretInfoMissing
}

func (odooDbConfig *OdooDatabaseConfig) GetDatabase(client client.Client, ctx context.Context, namespace string) (string, error) {
	// Use the DatabaseFromSecret if it is provided
	if odooDbConfig.NameFromSecret.Name != "" && odooDbConfig.NameFromSecret.Key != "" {
		database, err := utils.GetSecretValue(client, ctx, namespace, odooDbConfig.NameFromSecret.Name, odooDbConfig.NameFromSecret.Key)
		return database, err
	}
	// If DatabaseFromSecret is not provided, use the default database, which can also be given by the user
	return strings.TrimSpace(odooDbConfig.Name), nil
}

func (odooDbConfig *OdooDatabaseConfig) GetSSL(client client.Client, ctx context.Context, namespace string) (bool, error) {
	if odooDbConfig.SSLFromSecret.Name != "" && odooDbConfig.SSLFromSecret.Key != "" {
		ssl, err := utils.GetBoolSecretValue(client, ctx, namespace, odooDbConfig.SSLFromSecret.Name, odooDbConfig.SSLFromSecret.Key)
		return ssl, err
	}
	// If SSLModeFromSecret is not provided, use the default SSLMode
	return odooDbConfig.SSL, nil
}

func (odooDbConfig *OdooDatabaseConfig) GetMaxConn(client client.Client, ctx context.Context, namespace string) (int32, error) {
	if odooDbConfig.MaxConnFromSecret.Name != "" && odooDbConfig.MaxConnFromSecret.Key != "" {
		maxConnections, err := utils.GetInt32SecretValue(client, ctx, namespace, odooDbConfig.MaxConnFromSecret.Name, odooDbConfig.MaxConnFromSecret.Key)
		return maxConnections, err
	}
	// If MaxConnectionsFromSecret is not provided, use the default MaxConnections
	return odooDbConfig.MaxConn, nil
}

func (o *OdooDatabaseConfig) GetDbConnectionDetails(
	client client.Client,
	ctx context.Context,
	namespace string,
) (DatabaseConnectionDetails, error) {
	dbHost, err := o.GetHost(client, ctx, namespace)
	if err != nil {
		specifiedError := utils.ErrFailedToGetDbHost
		return DatabaseConnectionDetails{}, utilerrors.NewAggregate([]error{err, specifiedError})
	}
	dbPort, err := o.GetPort(client, ctx, namespace)
	if err != nil {
		specifiedError := utils.ErrFailedToGetDbPort
		return DatabaseConnectionDetails{}, utilerrors.NewAggregate([]error{err, specifiedError})
	}

	dbUser, err := o.GetUser(client, ctx, namespace)
	if err != nil {
		specifiedError := utils.ErrFailedToGetDbUser
		return DatabaseConnectionDetails{}, utilerrors.NewAggregate([]error{err, specifiedError})
	}
	dbPassword, err := o.GetPassword(client, ctx, namespace)
	if err != nil {
		specifiedError := utils.ErrFailedToGetDbPassword
		return DatabaseConnectionDetails{}, utilerrors.NewAggregate([]error{err, specifiedError})
	}
	dbName, err := o.GetDatabase(client, ctx, namespace)
	if err != nil {
		specifiedError := utils.ErrFailedToGetDbName
		return DatabaseConnectionDetails{}, utilerrors.NewAggregate([]error{err, specifiedError})
	}
	if dbName == "" {
		return DatabaseConnectionDetails{}, utils.ErrFailedToGetDbName
	}
	dbSsl, err := o.GetSSL(client, ctx, namespace)
	if err != nil {
		specifiedError := utils.ErrFailedToGetDbSslMode
		return DatabaseConnectionDetails{}, utilerrors.NewAggregate([]error{err, specifiedError})
	}

	dbMaxConn, err := o.GetMaxConn(client, ctx, namespace)
	if err != nil {
		specifiedError := utils.ErrFailedToGetDbMaxConns
		return DatabaseConnectionDetails{}, utilerrors.NewAggregate([]error{err, specifiedError})
	}

	return DatabaseConnectionDetails{
		Host:     dbHost,
		Port:     dbPort,
		User:     dbUser,
		Password: dbPassword,
		Name:     dbName,
		SSL:      dbSsl,
		MaxConn:  dbMaxConn,
	}, nil
}

// ---- odoo.conf ----------------------------------------------------------------

// SSLMode renders the db_sslmode value for the connection details.
func (d DatabaseConnectionDetails) SSLMode() string {
	if d.SSL {
		return "require"
	}
	return "disable"
}

// singleLine strips CR and LF so that a value can never break out of its odoo.conf line.
func singleLine(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// GetSerializedOdooConfig renders the [options] section of odoo.conf.
func (o *OdooConfig) GetSerializedOdooConfig(adminPassword string, db DatabaseConnectionDetails) string {
	var b strings.Builder
	write := func(key string, value any) {
		fmt.Fprintf(&b, "%s = %s\n", key, singleLine(fmt.Sprint(value)))
	}

	b.WriteString("[options]\n")
	write("admin_passwd", adminPassword)
	write("data_dir", o.DataDir)
	write("db_host", db.Host)
	write("db_port", db.Port)
	write("db_user", db.User)
	write("db_password", db.Password)
	write("db_maxconn", db.MaxConn)
	write("db_name", db.Name)
	write("db_sslmode", db.SSLMode())

	if o.ListDbValue() {
		write("list_db", "True")
		if o.DbFilter != "" {
			write("dbfilter", o.DbFilter)
		}
	} else {
		write("list_db", "False")
		if o.DbFilter != "" {
			write("dbfilter", o.DbFilter)
		} else {
			write("dbfilter", "^"+regexp.QuoteMeta(db.Name)+"$")
		}
	}

	write("debug_mode", o.DebugMode)
	write("without_demo", o.WithoutDemoValue())
	write("proxy_mode", o.ProxyModeValue())
	write("workers", o.WorkersValue())
	write("limit_memory_soft", o.LimitMemorySoft)
	write("limit_memory_hard", o.LimitMemoryHard)
	write("limit_request", o.LimitRequest)
	write("limit_time_cpu", o.LimitTimeCPU)
	write("limit_time_real", o.LimitTimeReal)
	write("max_cron_threads", o.MaxCronThreadsValue())

	if len(o.ServerWideModules) > 0 {
		write("server_wide_modules", strings.Join(o.ServerWideModules, ","))
	}
	if len(o.ExtraAddonsPaths) > 0 {
		write("addons_path", strings.Join(o.ExtraAddonsPaths, ","))
	}

	keys := make([]string, 0, len(o.ExtraOptions))
	for k := range o.ExtraOptions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		write(singleLine(k), string(o.ExtraOptions[k]))
	}
	return b.String()
}

var extraOptionKeyPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// InvalidExtraOptionKeys returns the extraOptions keys that are not valid
// odoo.conf option names, sorted. Map keys cannot be bounded in a CRD schema,
// which puts a CEL rule over them beyond the API server's cost budget, so the
// operator validates them at reconcile time instead.
func (o *OdooConfig) InvalidExtraOptionKeys() []string {
	var bad []string
	for k := range o.ExtraOptions {
		if !extraOptionKeyPattern.MatchString(k) {
			bad = append(bad, k)
		}
	}
	sort.Strings(bad)
	return bad
}

// ConfigHash returns the sha256 hex digest of a rendered odoo.conf. It is set
// as a pod-template annotation so that configuration changes roll the pods.
func ConfigHash(serializedConfig string) string {
	sum := sha256.Sum256([]byte(serializedConfig))
	return hex.EncodeToString(sum[:])
}

// ---- templates ------------------------------------------------------------------

// GetPvcName returns the filestore PVC name (status first, deterministic fallback).
func (o *OdooDeployment) GetPvcName() string {
	if o.Status.OdooDataPvcName != "" {
		return o.Status.OdooDataPvcName
	}
	return o.Name
}

// GetConfigSecretName returns the config Secret name (status first, deterministic fallback).
func (o *OdooDeployment) GetConfigSecretName() string {
	if o.Status.OdooConfigSecretName != "" {
		return o.Status.OdooConfigSecretName
	}
	return o.CreateOdooConfigSecretNamespacedName().Name
}

func (o *OdooDeployment) imagePullPolicy() corev1.PullPolicy {
	if o.Spec.ImagePullPolicy != "" {
		return o.Spec.ImagePullPolicy
	}
	return corev1.PullIfNotPresent
}

func (o *OdooDeployment) podSecurityContext() *corev1.PodSecurityContext {
	if o.Spec.PodSecurityContext != nil {
		return o.Spec.PodSecurityContext.DeepCopy()
	}
	return &corev1.PodSecurityContext{
		RunAsUser:    ptrTo(int64(100)),
		RunAsGroup:   ptrTo(int64(101)),
		RunAsNonRoot: ptrTo(true),
		FSGroup:      ptrTo(int64(101)),
	}
}

func ptrTo[T any](v T) *T { return &v }

func httpProbe(path string, initialDelay, period, timeout, failureThreshold int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   path,
				Port:   intstr.FromInt32(8069),
				Scheme: corev1.URISchemeHTTP,
			},
		},
		InitialDelaySeconds: initialDelay,
		PeriodSeconds:       period,
		TimeoutSeconds:      timeout,
		SuccessThreshold:    1,
		FailureThreshold:    failureThreshold,
	}
}

// DefaultReadinessProbe checks Odoo and its database connection.
func DefaultReadinessProbe() *corev1.Probe {
	return httpProbe("/web/health?db_server_status=1", 0, 10, 5, 3)
}

// DefaultLivenessProbe checks that the HTTP server answers.
func DefaultLivenessProbe() *corev1.Probe {
	return httpProbe("/web/health", 60, 30, 5, 6)
}

// DefaultStartupProbe gives Odoo up to 10 minutes to come up.
func DefaultStartupProbe() *corev1.Probe {
	return httpProbe("/web/health", 0, 10, 5, 60)
}

func (o *OdooDeployment) probes() (readiness, liveness, startup *corev1.Probe) {
	if !o.Spec.Probes.EnabledValue() {
		return nil, nil, nil
	}
	readiness, liveness, startup = DefaultReadinessProbe(), DefaultLivenessProbe(), DefaultStartupProbe()
	if o.Spec.Probes.Readiness != nil {
		readiness = o.Spec.Probes.Readiness.DeepCopy()
	}
	if o.Spec.Probes.Liveness != nil {
		liveness = o.Spec.Probes.Liveness.DeepCopy()
	}
	if o.Spec.Probes.Startup != nil {
		startup = o.Spec.Probes.Startup.DeepCopy()
	}
	return readiness, liveness, startup
}

// GetPodSpec renders the pod spec shared by the Deployment and the maintenance Jobs.
func (o *OdooDeployment) GetPodSpec() corev1.PodSpec {
	podRestartPolicy := corev1.RestartPolicyAlways
	podDNSPolicy := corev1.DNSClusterFirst
	terminationGracePeriodSeconds := int64(30)
	schedulerName := "default-scheduler"

	readiness, liveness, startup := o.probes()

	command := make([]string, 0, len(o.Spec.OdooCommand)+2)
	command = append(command, o.Spec.OdooCommand...)
	command = append(command, "-c", OdooConfigFile)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:            "odoo",
				Image:           o.Spec.Image,
				ImagePullPolicy: o.imagePullPolicy(),

				Command: command,
				Ports: []corev1.ContainerPort{
					{
						Name:          httpPortName,
						ContainerPort: 8069,
						Protocol:      corev1.ProtocolTCP,
					},
					{
						Name:          "poll",
						ContainerPort: 8072,
						Protocol:      corev1.ProtocolTCP,
					},
				},
				Env:       o.Spec.Env,
				EnvFrom:   o.Spec.EnvFrom,
				Resources: *o.Spec.Resources.DeepCopy(),
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      dataVolumeName,
						MountPath: fmt.Sprintf("%s/filestore", o.Spec.Config.DataDir),
						SubPath:   "filestore",
						ReadOnly:  false,
					},
					{
						Name:      dataVolumeName,
						MountPath: fmt.Sprintf("%s/sessions", o.Spec.Config.DataDir),
						SubPath:   "sessions",
						ReadOnly:  false,
					},
					{
						Name:      "config",
						MountPath: OdooConfigMountPath,
						ReadOnly:  true,
					},
				},
				ReadinessProbe:           readiness,
				LivenessProbe:            liveness,
				StartupProbe:             startup,
				TerminationMessagePath:   "/dev/termination-log",
				TerminationMessagePolicy: corev1.TerminationMessageReadFile,
			},
		},
		Volumes: []corev1.Volume{
			{
				Name: dataVolumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: o.GetPvcName(),
					},
				},
			},
			{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: o.GetConfigSecretName(),
						Items: []corev1.KeyToPath{
							{
								Key:  odooConfigKey,
								Path: odooConfigKey,
							},
						},
						DefaultMode: ptrTo(int32(0444)),
					},
				},
			},
		},
		SecurityContext:               o.podSecurityContext(),
		ImagePullSecrets:              o.Spec.ImagePullSecrets,
		RestartPolicy:                 podRestartPolicy,
		DNSPolicy:                     podDNSPolicy,
		TerminationGracePeriodSeconds: &terminationGracePeriodSeconds,
		SchedulerName:                 schedulerName,
	}
	return podSpec
}

// dedupe removes duplicate entries in place, preserving the order of first occurrences.
func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	unique := make([]string, 0, len(in))
	for _, m := range in {
		if _, exists := seen[m]; !exists {
			seen[m] = struct{}{}
			unique = append(unique, m)
		}
	}
	return unique
}

// DeduplicateModules removes duplicate entries from Spec.Modules and
// Spec.Upgrade.Modules in place, preserving the original order of first occurrences.
func (o *OdooDeployment) DeduplicateModules() {
	o.Spec.Modules = dedupe(o.Spec.Modules)
	if o.Spec.Upgrade.Modules != nil {
		o.Spec.Upgrade.Modules = dedupe(o.Spec.Upgrade.Modules)
	}
}

// GetInitJobName is the name of the first-boot maintenance Job.
func (o *OdooDeployment) GetInitJobName() string {
	return fmt.Sprintf("%s-init", o.Name)
}

// MaintenanceJobName returns "<name>-init" on first boot and a stable
// "<name>-upgrade-<hash>" otherwise, where the hash covers the image, the
// upgrade token and the modules to install so that a retried reconcile adopts
// the Job it already created.
func (o *OdooDeployment) MaintenanceJobName(initModules []string, firstBoot bool) string {
	if firstBoot {
		return o.GetInitJobName()
	}
	h := sha256.New()
	h.Write([]byte(o.Spec.Image))
	h.Write([]byte{0})
	h.Write([]byte(o.Spec.Upgrade.Token))
	h.Write([]byte{0})
	h.Write([]byte(strings.Join(initModules, ",")))
	return fmt.Sprintf("%s-upgrade-%s", o.Name, hex.EncodeToString(h.Sum(nil))[:8])
}

// GetMaintenanceJobTemplate renders the Job that installs (-i) and/or
// upgrades (-u) modules on spec.image. --load-language is only passed when
// modules are installed.
func (o *OdooDeployment) GetMaintenanceJobTemplate(initModules, upgradeModules []string, firstBoot bool) batchv1.Job {
	spec := o.GetPodSpec()
	container := &spec.Containers[0]

	command := make([]string, 0, len(o.Spec.OdooCommand)+8)
	command = append(command, o.Spec.OdooCommand...)
	command = append(command, "-c", OdooConfigFile, "--stop-after-init", "--no-http")
	if len(initModules) > 0 {
		command = append(command, "-i", strings.Join(initModules, ","))
	}
	if len(upgradeModules) > 0 {
		command = append(command, "-u", strings.Join(upgradeModules, ","))
	}
	if len(initModules) > 0 && len(o.Spec.Config.LoadLanguages) > 0 {
		command = append(command, "--load-language="+strings.Join(o.Spec.Config.LoadLanguages, ","))
	}
	container.Command = command
	container.Ports = nil
	container.ReadinessProbe = nil
	container.LivenessProbe = nil
	container.StartupProbe = nil
	spec.RestartPolicy = corev1.RestartPolicyNever

	kind := JobKindUpgrade
	if firstBoot {
		kind = JobKindInit
	}

	job := batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.MaintenanceJobName(initModules, firstBoot),
			Namespace: o.Namespace,
			Labels: map[string]string{
				LabelOdooDeployment: o.Name,
				LabelJobKind:        kind,
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						LabelOdooDeployment: o.Name,
						LabelJobKind:        kind,
					},
				},
				Spec: spec,
			},
			Parallelism:             ptrTo(int32(1)),
			Completions:             ptrTo(int32(1)),
			BackoffLimit:            ptrTo(o.Spec.Jobs.BackoffLimitValue()),
			ActiveDeadlineSeconds:   ptrTo(o.Spec.Jobs.ActiveDeadlineSecondsValue()),
			TTLSecondsAfterFinished: ptrTo(o.Spec.Jobs.TTLSecondsAfterFinishedValue()),
		},
	}
	return job
}

func (o *OdooDeployment) GetPvcTemplate() corev1.PersistentVolumeClaim {
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.Name,
			Namespace: o.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &o.Spec.OdooFilestore.StorageClassName,
			AccessModes:      o.Spec.OdooFilestore.AccessModes,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: o.Spec.OdooFilestore.Size,
				},
			},
		},
	}
	return pvc
}

func (o *OdooDeployment) CreateOdooConfigSecretNamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-config", o.Name),
		Namespace: o.Namespace,
	}
}

func (o *OdooDeployment) CreateOdooAdminPasswordSecretNamespacedName() types.NamespacedName {
	if o.Spec.Config.AdminPasswordSecretName != "" {
		return types.NamespacedName{
			Name:      o.Spec.Config.AdminPasswordSecretName,
			Namespace: o.Namespace,
		}
	}
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-admin-password", o.Name),
		Namespace: o.Namespace,
	}
}

// AdminPasswordSecretIsUserNamed reports whether the admin password Secret
// was named by the user (and therefore is not owned by the OdooDeployment).
func (o *OdooDeployment) AdminPasswordSecretIsUserNamed() bool {
	return o.Spec.Config.AdminPasswordSecretName != ""
}

func (o *OdooDeployment) GetHttpServiceName() string {
	return fmt.Sprintf("%s-http", o.Name)
}
func (o *OdooDeployment) GetPollServiceName() string {
	return fmt.Sprintf("%s-poll", o.Name)
}

// GetServiceSelectorLabels is the immutable selector shared by the Deployment
// and the Services. Maintenance Job pods never carry it.
func (o *OdooDeployment) GetServiceSelectorLabels() map[string]string {
	return map[string]string{
		"app": o.Name,
	}
}

// GetPodLabels are the labels on the Deployment pod template.
func (o *OdooDeployment) GetPodLabels() map[string]string {
	labels := o.GetServiceSelectorLabels()
	labels[LabelOdooDeployment] = o.Name
	return labels
}

func (o *OdooDeployment) GetHttpServiceTemplate() corev1.Service {
	internalTrafficPolicy := corev1.ServiceInternalTrafficPolicyCluster
	service := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.GetHttpServiceName(),
			Namespace: o.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: o.GetServiceSelectorLabels(),
			Ports: []corev1.ServicePort{
				{
					Name:       httpPortName,
					Port:       8069,
					TargetPort: intstr.FromInt32(8069),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Type:                  corev1.ServiceTypeClusterIP,
			SessionAffinity:       corev1.ServiceAffinityNone,
			InternalTrafficPolicy: &internalTrafficPolicy,
		},
	}
	return service
}

func (o *OdooDeployment) GetPollServiceTemplate() corev1.Service {
	internalTrafficPolicy := corev1.ServiceInternalTrafficPolicyCluster
	service := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.GetPollServiceName(),
			Namespace: o.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: o.GetServiceSelectorLabels(),
			Ports: []corev1.ServicePort{
				{
					Name:       httpPortName,
					Port:       8072,
					TargetPort: intstr.FromInt32(8072),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Type:                  corev1.ServiceTypeClusterIP,
			SessionAffinity:       corev1.ServiceAffinityNone,
			InternalTrafficPolicy: &internalTrafficPolicy,
		},
	}
	return service
}

// GetDeploymentTemplate renders the Deployment for a given image and replica
// count (the reconciler decides both: status.appliedImage, and 0 while an
// upgrade Job runs). The strategy is Recreate because the filestore PVC is
// ReadWriteOnce and there is exactly one database.
func (o *OdooDeployment) GetDeploymentTemplate(image string, replicas int32, configHash string) appsv1.Deployment {
	revisionHistoryLimit := int32(10)
	progressDeadlineSeconds := int32(600)
	podSpec := o.GetPodSpec()
	podSpec.Containers[0].Image = image
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.Name,
			Namespace: o.Namespace,
			Labels:    o.GetPodLabels(),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: o.GetServiceSelectorLabels(),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: o.GetPodLabels(),
					Annotations: map[string]string{
						AnnotationConfigHash: configHash,
					},
				},
				Spec: podSpec,
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			RevisionHistoryLimit:    &revisionHistoryLimit,
			ProgressDeadlineSeconds: &progressDeadlineSeconds,
		},
	}
}

// UsesSecret checks whether a given secret is used by a Cluster.
//
// This function is also used to discover the set of clusters that
// should be reconciled when a certain secret changes.
func (o *OdooDeployment) UsesSecret(secret string) bool {
	switch secret {
	case o.Spec.Database.HostFromSecret.Name:
		return true
	case o.Spec.Database.PortFromSecret.Name:
		return true
	case o.Spec.Database.UserFromSecret.Name:
		return true
	case o.Spec.Database.PasswordFromSecret.Name:
		return true
	case o.Spec.Database.NameFromSecret.Name:
		return true
	case o.Spec.Database.SSLFromSecret.Name:
		return true
	case o.Spec.Database.MaxConnFromSecret.Name:
		return true
	case o.Spec.Config.AdminPasswordSecretName:
		return true
	default:
		return false
	}
}

func (o *OdooDeployment) UsesPVC(pvcName string) bool {
	return o.Status.OdooDataPvcName == pvcName
}

func (o *OdooDeployment) UsesDeployment(deploymentName string) bool {
	return o.Name == deploymentName
}

func (o *OdooDeployment) UsesService(serviceName string) bool {
	return o.GetHttpServiceName() == serviceName || o.GetPollServiceName() == serviceName
}
