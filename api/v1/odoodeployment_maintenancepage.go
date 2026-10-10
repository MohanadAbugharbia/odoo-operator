package v1

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	// DefaultMaintenancePageTitle and DefaultMaintenancePageAccentColor mirror
	// the CRD defaults of spec.maintenancePage.
	DefaultMaintenancePageTitle       = "Odoo"
	DefaultMaintenancePageAccentColor = "#714b67"

	// MaintenancePagePort is the port the maintenance server listens on. It
	// is Odoo's HTTP port, so the http Service only changes its selector.
	MaintenancePagePort int32 = 8069
	// MaintenancePagePathPrefix holds the maintenance server's own routes.
	MaintenancePagePathPrefix = "/__maintenance/"
	// maintenancePageContainer is the container name in the maintenance pod.
	maintenancePageContainer = "maintenance-page"
	// MaintenancePageStatusKey is the ConfigMap key holding the page's
	// snapshot, mounted at MaintenancePageStatusMountPath.
	MaintenancePageStatusKey       = "status.json"
	MaintenancePageStatusMountPath = "/etc/odoo-maintenance-page"
	maintenancePageStatusVolume    = "status"
)

// TitleValue returns spec.maintenancePage.title or the CRD default.
func (m *MaintenancePageConfig) TitleValue() string {
	if m.Title == "" {
		return DefaultMaintenancePageTitle
	}
	return string(m.Title)
}

// TitleFor returns the title for a page language, falling back to TitleValue.
func (m *MaintenancePageConfig) TitleFor(lang string) string {
	if t := m.TitleTranslations[lang]; t != "" {
		return string(t)
	}
	return m.TitleValue()
}

// AccentColorValue returns spec.maintenancePage.accentColor or the CRD default.
func (m *MaintenancePageConfig) AccentColorValue() string {
	if m.AccentColor == "" {
		return DefaultMaintenancePageAccentColor
	}
	return m.AccentColor
}

// MaintenancePageName names the maintenance Deployment and the ConfigMap the
// operator writes the page's status into.
func (o *OdooDeployment) MaintenancePageName() string {
	return o.Name + "-maintenance"
}

// GetMaintenancePageSelectorLabels selects the maintenance pods; the http
// Service uses it while the page is served.
func (o *OdooDeployment) GetMaintenancePageSelectorLabels() map[string]string {
	return map[string]string{LabelMaintenancePage: o.Name}
}

// GetMaintenancePageDeploymentTemplate renders the Deployment that serves the
// maintenance page from the operator's own image. The page reads its status
// from the mounted <name>-maintenance ConfigMap, so the pod needs no access to
// the API server (preview namespaces deny that egress) and no token. It asks
// for little enough to fit a namespace quota sized for the Odoo pods alone.
func (o *OdooDeployment) GetMaintenancePageDeploymentTemplate(image string, replicas int32) appsv1.Deployment {
	labels := o.GetMaintenancePageSelectorLabels()
	probe := &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
			Path:   MaintenancePagePathPrefix + "healthz",
			Port:   intstr.FromInt32(MaintenancePagePort),
			Scheme: corev1.URISchemeHTTP,
		}},
		PeriodSeconds:    5,
		TimeoutSeconds:   2,
		FailureThreshold: 3,
		SuccessThreshold: 1,
	}
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.MaintenancePageName(),
			Namespace: o.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptrTo(false),
					Containers: []corev1.Container{{
						Name:            maintenancePageContainer,
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"/odoo-operator", "maintenance-page"},
						Args: []string{
							"--status-file=" + MaintenancePageStatusMountPath + "/" + MaintenancePageStatusKey,
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      maintenancePageStatusVolume,
							MountPath: MaintenancePageStatusMountPath,
							ReadOnly:  true,
						}},
						Ports: []corev1.ContainerPort{{
							Name:          httpPortName,
							ContainerPort: MaintenancePagePort,
							Protocol:      corev1.ProtocolTCP,
						}},
						ReadinessProbe: probe,
						LivenessProbe:  probe.DeepCopy(),
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10m"),
								corev1.ResourceMemory: resource.MustParse("32Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("64Mi"),
							},
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptrTo(false),
							ReadOnlyRootFilesystem:   ptrTo(true),
							RunAsNonRoot:             ptrTo(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
						TerminationMessagePath:   corev1.TerminationMessagePathDefault,
						TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
					}},
					Volumes: []corev1.Volume{{
						Name: maintenancePageStatusVolume,
						VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: o.MaintenancePageName()},
							DefaultMode:          ptrTo(int32(0444)),
							// The page shows "unavailable" until the
							// operator has written it.
							Optional: ptrTo(true),
						}},
					}},
					RestartPolicy:                 corev1.RestartPolicyAlways,
					DNSPolicy:                     corev1.DNSClusterFirst,
					TerminationGracePeriodSeconds: ptrTo(int64(10)),
				},
			},
			RevisionHistoryLimit: ptrTo(int32(2)),
		},
	}
}
