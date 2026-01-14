package reconcileloops

import (
	"context"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("Deployment Reconcile Loop", func() {
	var (
		reconciler     *DeploymentReconciler
		ctx            context.Context
		req            reconcile.Request
		odooDeployment *odoov1.OdooDeployment
	)
	const resourceName = "test-odoo-deployment"
	const resourceNamespace = "default"

	ctx = context.Background()

	typeNamespacedName := types.NamespacedName{
		Name:      resourceName,
		Namespace: resourceNamespace,
	}
	requestedReplicas := int32(2)

	BeforeEach(func() {
		By("creating the custom resource for the Kind OdooDeployment")
		odooDeployment = &odoov1.OdooDeployment{}
		err := k8sClient.Get(ctx, typeNamespacedName, odooDeployment)
		if err != nil && errors.IsNotFound(err) {
			odooDeployment = &odoov1.OdooDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: resourceNamespace,
				},
				Spec: odoov1.OdooDeploymentSpec{
					Replicas: requestedReplicas,
					Image:    "mohanadabugharbia/odoo:18",
					OdooFilestore: odoov1.PersistentVolumeClaimSpec{
						Size: resource.MustParse("1Gi"),
						Name: "test-odoo-filestore-pvc",
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
					},
					Database: odoov1.OdooDatabaseConfig{
						HostFromSecret: corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-db-secret",
							},
							Key: "host",
						},
						PortFromSecret: corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-db-secret",
							},
							Key: "port",
						},
						UserFromSecret: corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-db-secret",
							},
							Key: "user",
						},
						PasswordFromSecret: corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-db-secret",
							},
							Key: "password",
						},
						NameFromSecret: corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-db-secret",
							},
							Key: "name",
						},
						SSLFromSecret: corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-db-secret",
							},
							Key: "ssl",
						},
						MaxConnFromSecret: corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-db-secret",
							},
							Key: "maxconn",
						},
					},
					Modules: []string{
						"base",
						"web",
					},
				},
			}
			Expect(k8sClient.Create(ctx, odooDeployment)).To(Succeed())
		}

		By("Creating the PVC for the OdooDeployment's filestore")
		pvc := odooDeployment.GetPvcTemplate()
		err = k8sClient.Get(ctx, types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}, &corev1.PersistentVolumeClaim{})
		if err != nil && errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, &pvc)).To(Succeed())
		}
		odooDeployment.Status.OdooDataPvcName = pvc.Name
		Expect(k8sClient.Status().Update(ctx, odooDeployment)).To(Succeed())
		Expect(odooDeployment.Status.OdooDataPvcName).To(Equal(pvc.Name))
		By("Creating database secret for the OdooDeployment")
		dbSecret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-db-secret",
				Namespace: resourceNamespace,
			},
			Data: map[string][]byte{
				"host":     []byte("db-host"),
				"port":     []byte("5432"),
				"user":     []byte("db-user"),
				"password": []byte("db-password"),
				"name":     []byte("db-name"),
				"ssl":      []byte("false"),
				"maxconn":  []byte("20"),
			},
		}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: dbSecret.Name, Namespace: dbSecret.Namespace}, &corev1.Secret{})
		if err != nil && errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, &dbSecret)).To(Succeed())
		}
		By("Creating config secret for the OdooDeployment")
		secret, err := odooDeployment.CreateOdooConfigSecretObj(k8sClient, ctx, "admin123")
		Expect(err).NotTo(HaveOccurred())
		err = k8sClient.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, &corev1.Secret{})
		if err != nil && errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, &secret)).To(Succeed())
			odooDeployment.Status.OdooConfigSecretName = secret.Name
			Expect(k8sClient.Status().Update(ctx, odooDeployment)).To(Succeed())
		}
	})

	AfterEach(func() {
		By("Deleting the Deployment created by the Reconciler")
		deployment := odooDeployment.GetDeploymentTemplate()
		err := k8sClient.Get(ctx, types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}, &deployment)
		if err == nil {
			Expect(k8sClient.Delete(ctx, &deployment)).To(Succeed())
		}
		By("Deleting the config secret created for the OdooDeployment")
		secret, err := odooDeployment.CreateOdooConfigSecretObj(k8sClient, ctx, "admin123")
		Expect(err).NotTo(HaveOccurred())
		err = k8sClient.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, &secret)
		if err == nil {
			Expect(k8sClient.Delete(ctx, &secret)).To(Succeed())
		}
		By("Deleting the PVC created for the OdooDeployment's filestore")
		pvc := odooDeployment.GetPvcTemplate()
		err = k8sClient.Get(ctx, types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}, &pvc)
		if err == nil {
			Expect(k8sClient.Delete(ctx, &pvc)).To(Succeed())
		}
		By("deleting the custom resource for the Kind OdooDeployment")
		err = k8sClient.Get(ctx, typeNamespacedName, odooDeployment)
		if err == nil {
			Expect(k8sClient.Delete(ctx, odooDeployment)).To(Succeed())
		}
		By("Deleting the database secret created for the OdooDeployment")
		dbSecret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-db-secret",
				Namespace: resourceNamespace,
			},
		}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: dbSecret.Name, Namespace: dbSecret.Namespace}, &dbSecret)
		if err == nil {
			Expect(k8sClient.Delete(ctx, &dbSecret)).To(Succeed())
		}
	})

	It("should return a Deployment", func() {
		reconciler = &DeploymentReconciler{
			Client:         k8sClient,
			Scheme:         k8sClient.Scheme(),
			OdooDeployment: odooDeployment,
		}

		req = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      "test-odoo-deployment",
				Namespace: "default",
			},
		}
		deployment, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(deployment.Name).To(Equal("test-odoo-deployment"))
	})
	It("should have the correct number of replicas", func() {
		reconciler = &DeploymentReconciler{
			Client:         k8sClient,
			Scheme:         k8sClient.Scheme(),
			OdooDeployment: odooDeployment,
		}

		req = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      "test-odoo-deployment",
				Namespace: "default",
			},
		}
		deployment, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(*deployment.Spec.Replicas).To(Equal(requestedReplicas))
	})
	It("should have the correct image in atleast one container", func() {
		reconciler = &DeploymentReconciler{
			Client:         k8sClient,
			Scheme:         k8sClient.Scheme(),
			OdooDeployment: odooDeployment,
		}

		req = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      "test-odoo-deployment",
				Namespace: "default",
			},
		}
		deployment, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		found := false
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Image == "mohanadabugharbia/odoo:18" {
				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), "Expected to find container with image mohanadabugharbia/odoo:18")
	})
	It("Should have correct owner references", func() {
		reconciler = &DeploymentReconciler{
			Client:         k8sClient,
			Scheme:         k8sClient.Scheme(),
			OdooDeployment: odooDeployment,
		}

		req = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      "test-odoo-deployment",
				Namespace: "default",
			},
		}
		deployment, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(deployment.OwnerReferences)).To(Equal(1))
		Expect(deployment.OwnerReferences[0].Kind).To(Equal("OdooDeployment"))
		Expect(deployment.OwnerReferences[0].Name).To(Equal(odooDeployment.Name))
	})
	It("Should update deployment when OdooDeployment spec changes", func() {
		reconciler = &DeploymentReconciler{
			Client:         k8sClient,
			Scheme:         k8sClient.Scheme(),
			OdooDeployment: odooDeployment,
		}

		req = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      "test-odoo-deployment",
				Namespace: "default",
			},
		}
		deployment, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(*deployment.Spec.Replicas).To(Equal(requestedReplicas))

		// Update the OdooDeployment spec
		err = k8sClient.Get(ctx, typeNamespacedName, odooDeployment)
		Expect(err).NotTo(HaveOccurred())
		newReplicas := int32(3)
		odooDeployment.Spec.Replicas = newReplicas
		err = k8sClient.Update(ctx, odooDeployment)
		Expect(err).NotTo(HaveOccurred())

		// Reconcile again
		reconciler.OdooDeployment = odooDeployment
		deployment, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(*deployment.Spec.Replicas).To(Equal(newReplicas))
	})
})
