package v1

import (
	"context"
	"fmt"

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

func (odooDbConfig *OdooDatabaseConfig) GetHost(client client.Client, ctx context.Context, namespace string) (string, error) {
	// Use the HostFromSecret if it is provided
	if odooDbConfig.HostFromSecret.Name != "" && odooDbConfig.HostFromSecret.Key != "" {
		host, err := utils.GetSecretValue(client, ctx, namespace, odooDbConfig.HostFromSecret.Name, odooDbConfig.HostFromSecret.Key)
		return host, err
	}
	// If HostFromSecret is not provided, use the default host, which can also be given by the user
	return odooDbConfig.Host, nil
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
	return odooDbConfig.User, nil
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
	return odooDbConfig.Name, nil
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

func (o *OdooDeployment) GetPodSpec() corev1.PodSpec {
	podRestartPolicy := corev1.RestartPolicyAlways
	podDNSPolicy := corev1.DNSClusterFirst
	terminationGracePeriodSeconds := int64(30)
	schedulerName := "default-scheduler"

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:            "odoo",
				Image:           o.Spec.Image,
				ImagePullPolicy: o.Spec.ImagePullPolicy,
				Command: []string{
					"/entrypoint.sh",
					"-c",
					"/opt/odoo/odoo.conf",
				},
				Ports: []corev1.ContainerPort{
					{
						Name:          "http",
						ContainerPort: 8069,
						Protocol:      "TCP",
					},
					{
						Name:          "poll",
						ContainerPort: 8072,
						Protocol:      "TCP",
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "odoo-data",
						MountPath: fmt.Sprintf("%s/filestore", o.Spec.Config.DataDir),
						SubPath:   "filestore",
						ReadOnly:  false,
					},
					{
						Name:      "odoo-data",
						MountPath: fmt.Sprintf("%s/sessions", o.Spec.Config.DataDir),
						SubPath:   "sessions",
						ReadOnly:  false,
					},
					{
						Name:      "config",
						MountPath: "/opt/odoo",
						ReadOnly:  true,
					},
				},
				TerminationMessagePath:   "/dev/termination-log",
				TerminationMessagePolicy: corev1.TerminationMessageReadFile,
			},
		},
		Volumes: []corev1.Volume{
			{
				Name: "odoo-data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: o.Status.OdooDataPvcName,
					},
				},
			},
			{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: o.Status.OdooConfigSecretName,
						Items: []corev1.KeyToPath{
							{
								Key:  "odoo.conf",
								Path: "odoo.conf",
							},
						},
						DefaultMode: func(i int32) *int32 { return &i }(0444),
					},
				},
			},
		},
		SecurityContext: &corev1.PodSecurityContext{
			RunAsUser:    func(i int64) *int64 { return &i }(100),
			RunAsGroup:   func(i int64) *int64 { return &i }(101),
			RunAsNonRoot: func(i bool) *bool { return &i }(true),
			FSGroup:      func(i int64) *int64 { return &i }(101),
		},
		RestartPolicy:                 podRestartPolicy,
		DNSPolicy:                     podDNSPolicy,
		TerminationGracePeriodSeconds: &terminationGracePeriodSeconds,
		SchedulerName:                 schedulerName,
	}
	return podSpec
}

func (o *OdooDeployment) GetDbInitJobTemplate() (batchv1.Job, []string) {
	stringFormattedInitModules := ""
	// Add the init modules to the string
	for _, module := range o.Spec.Modules {
		stringFormattedInitModules += fmt.Sprintf("%s,", module)
	}
	// Remove the last comma
	stringFormattedInitModules = stringFormattedInitModules[:len(stringFormattedInitModules)-1]

	spec := o.GetPodSpec()
	spec.Containers[0].Command = []string{
		"/entrypoint.sh",
		"-c",
		"/opt/odoo/odoo.conf",
		"--stop-after-init",
		"--no-http",
		"--init",
		stringFormattedInitModules,
	}
	spec.Containers[0].Ports = []corev1.ContainerPort{}
	spec.RestartPolicy = corev1.RestartPolicyNever

	job := batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-init", o.Name),
			Namespace: o.Namespace,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: spec,
			},
			Parallelism:  func(i int32) *int32 { return &i }(1),
			BackoffLimit: func(i int32) *int32 { return &i }(2),
		},
	}
	return job, o.Spec.Modules
}

func (o *OdooConfig) GetSerializedOdooConfig(
	adminPassword string,
	dbHost string,
	dbPort int32,
	dbUser string,
	dbPassword string,
	dbMaxConn int32,
	dbName string,
) string {
	return fmt.Sprintf(
		"[options]\nadmin_passwd = %s\ndata_dir=%s\n\ndb_host = %s\ndb_port = %d\ndb_user = %s\ndb_password = %s\ndb_maxconn = %d\ndb_name= %s\n\ndebug_mode = %t\nwithout_demo = %t\nproxy_mode = %t\nworkers = %d\nlimit_memory_soft = %d\nlimit_memory_hard = %d\nlimit_request = %d\nlimit_time_cpu = %d\nlimit_time_real = %d\n",
		adminPassword,
		o.DataDir,
		dbHost,
		dbPort,
		dbUser,
		dbPassword,
		dbMaxConn,
		dbName,
		o.DebugMode,
		o.WithoutDemo,
		o.ProxyMode,
		o.Workers,
		o.LimitMemorySoft,
		o.LimitMemoryHard,
		o.LimitRequest,
		o.LimitTimeCPU,
		o.LimitTimeReal,
	)
}

func (o *OdooDeployment) GetOdooConfigSecretTemplate(serializedOdooConfig string) corev1.Secret {
	odooConfigSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.Name,
			Namespace: o.Namespace,
		},
		Data: map[string][]byte{
			"odoo.conf": []byte(serializedOdooConfig),
		},
	}
	return odooConfigSecret
}

func (o *OdooDeployment) GetPvcTemplate() corev1.PersistentVolumeClaim {
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.Name,
			Namespace: o.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: o.Spec.OdooFilestore.AccessModes,
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

// TODO: Fix this function. Implementation suggestion: Pass a reference to the secret to be created
// func (o *OdooDeployment) CreateOdooConfigSecret(
// 	client client.Client,
// 	ctx context.Context,
// ) (corev1.Secret, error) {

// 	secretNamespacedName := o.CreateOdooConfigSecretNamespacedName()
// 	dbConnectionDetails, err := o.Spec.Database.GetDbConnectionDetails(client, ctx, o.Namespace)
// 	if err != nil {
// 		return corev1.Secret{}, err
// 	}

// 	serializedOdooConfig := o.Spec.Config.GetSerializedOdooConfig(
// 		"admin",
// 		dbConnectionDetails.Host,
// 		dbConnectionDetails.Port,
// 		dbConnectionDetails.User,
// 		dbConnectionDetails.Password,
// 		dbConnectionDetails.MaxConn,
// 		dbConnectionDetails.Name,
// 	)

// 	secret := o.GetOdooConfigSecretTemplate(serializedOdooConfig)

// 	secret.Name = secretNamespacedName.Name
// 	secret.Namespace = secretNamespacedName.Namespace

// 	return secret, nil
// }

func (o *OdooConfig) GetOdooAdminPasswordSecretTemplate(
	defaultSecretName string,
	defaultSecretNamespace string,
) (corev1.Secret, error) {
	randomPassword, err := utils.GenerateSecurePassword()
	if err != nil {
		return corev1.Secret{}, err
	}
	secretName := ""
	secretKey := "password"
	if o.AdminPasswordSecretName != "" {
		secretName = o.AdminPasswordSecretName
	} else {
		secretName = defaultSecretName
	}
	return corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: defaultSecretNamespace,
		},
		Data: map[string][]byte{
			secretKey: []byte(randomPassword),
		},
	}, nil
}

func (o *OdooDeployment) GetHttpServiceName() string {
	return fmt.Sprintf("%s-http", o.Name)
}
func (o *OdooDeployment) GetPollServiceName() string {
	return fmt.Sprintf("%s-poll", o.Name)
}

func (o *OdooDeployment) GetServiceSelectorLabels() map[string]string {
	return map[string]string{
		"app": o.Name,
	}
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
					Name:       "http",
					Port:       8069,
					TargetPort: intstr.FromInt(8069),
					Protocol:   "TCP",
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
					Name:       "http",
					Port:       8072,
					TargetPort: intstr.FromInt(8072),
					Protocol:   "TCP",
				},
			},
			Type:                  corev1.ServiceTypeClusterIP,
			SessionAffinity:       corev1.ServiceAffinityNone,
			InternalTrafficPolicy: &internalTrafficPolicy,
		},
	}
	return service
}

func (o *OdooDeployment) GetDeploymentTemplate() appsv1.Deployment {
	maxUnavailable := intstr.FromString("25%")
	maxSurge := intstr.FromString("25%")
	revisionHistoryLimit := int32(10)
	progressDeadlineSeconds := int32(600)
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.Name,
			Namespace: o.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &o.Spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: o.GetServiceSelectorLabels(),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: o.GetServiceSelectorLabels(),
				},
				Spec: o.GetPodSpec(),
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &maxUnavailable,
					MaxSurge:       &maxSurge,
				},
			},
			RevisionHistoryLimit:    &revisionHistoryLimit,
			ProgressDeadlineSeconds: &progressDeadlineSeconds,
		},
	}
}

func (o *OdooDeployment) CreateOdooConfigSecretObj(
	client client.Client,
	ctx context.Context,
	adminPassword string,
) (corev1.Secret, error) {
	dbConnectionDetails, err := o.Spec.Database.GetDbConnectionDetails(client, ctx, o.Namespace)
	if err != nil {
		return corev1.Secret{}, err
	}

	serializedOdooConfig := o.Spec.Config.GetSerializedOdooConfig(
		string(adminPassword),
		dbConnectionDetails.Host,
		dbConnectionDetails.Port,
		dbConnectionDetails.User,
		dbConnectionDetails.Password,
		dbConnectionDetails.MaxConn,
		dbConnectionDetails.Name,
	)

	return o.GetOdooConfigSecretTemplate(serializedOdooConfig), nil
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
