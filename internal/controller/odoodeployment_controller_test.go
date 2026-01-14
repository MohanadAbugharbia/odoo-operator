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

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

var _ = Describe("OdooDeployment Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"
		const resourceNamespace = "default"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}
		odoodeployment := &odoov1.OdooDeployment{}

		databaseSecretTypeNamespacedName := types.NamespacedName{
			Name:      "my-db-secret",
			Namespace: resourceNamespace,
		}

		databaseSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-db-secret",
				Namespace: resourceNamespace,
			},
			Data: map[string][]byte{
				"password": []byte("my-secret-password"),
			},
		}

		BeforeEach(func() {
			By("creating the database secret")
			Expect(k8sClient.Create(ctx, databaseSecret)).To(Succeed())

			By("creating the custom resource for the Kind OdooDeployment")
			err := k8sClient.Get(ctx, typeNamespacedName, odoodeployment)
			if err != nil && errors.IsNotFound(err) {
				resource := &odoov1.OdooDeployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: resourceNamespace,
					},
					Spec: odoov1.OdooDeploymentSpec{
						Name:     "my-odoo-deployment",
						Image:    "mohanadabugharbia/odoo:18",
						Replicas: 1,
						Database: odoov1.OdooDatabaseConfig{
							Host: "my-database-host",
							Port: 5432,
							User: "my-db-user",
							Name: "my-database-name",
							PasswordFromSecret: corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "my-db-secret",
								},
								Key: "password",
							},
						},
						OdooFilestore: odoov1.PersistentVolumeClaimSpec{
							Name: "my-odoo-filestore-pvc",
							Size: resource.MustParse("10Gi"),
							AccessModes: []corev1.PersistentVolumeAccessMode{
								corev1.ReadWriteOnce,
							},
						},
						Modules: []string{
							"base",
							"web",
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &odoov1.OdooDeployment{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance OdooDeployment")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			databaseSecretResource := &corev1.Secret{}
			err = k8sClient.Get(ctx, databaseSecretTypeNamespacedName, databaseSecretResource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the database secret")
			Expect(k8sClient.Delete(ctx, databaseSecretResource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &OdooDeploymentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})

var _ = Describe("Filtering OdooDeployments by PVC", func() {
	odooDeployment := odoov1.OdooDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-odoo-deployment",
			Namespace: "default",
		},
		Spec: odoov1.OdooDeploymentSpec{
			Name: "my-odoo-deployment",
		},
		Status: odoov1.OdooDeploymentStatus{
			OdooDataPvcName: "my-pvc-name",
		},
	}

	items := []odoov1.OdooDeployment{
		odooDeployment,
	}

	odooDeploymentList := odoov1.OdooDeploymentList{
		Items: items,
	}

	It("Not using a PVC", func() {
		pvc := corev1.PersistentVolumeClaim{}
		pvc.Name = "another-pvc-name"
		req := filterOdooDeploymentsUsingPVC(odooDeploymentList, &pvc)
		Expect(len(req)).To(Equal(0))
	})

	It("Using a PVC", func() {
		pvc := corev1.PersistentVolumeClaim{}
		pvc.Name = "my-pvc-name"
		req := filterOdooDeploymentsUsingPVC(odooDeploymentList, &pvc)
		Expect(len(req)).To(Equal(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
})

var _ = Describe("Filtering OdooDeployments by Secret", func() {
	odooDeployment := odoov1.OdooDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-odoo-deployment",
			Namespace: "default",
		},
		Spec: odoov1.OdooDeploymentSpec{
			Name: "my-odoo-deployment",
			Database: odoov1.OdooDatabaseConfig{
				HostFromSecret: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "my-db-host-secret",
					},
					Key: "host",
				},
				PortFromSecret: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "my-db-port-secret",
					},
					Key: "port",
				},
				UserFromSecret: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "my-db-user-secret",
					},
					Key: "user",
				},
				PasswordFromSecret: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "my-db-password-secret",
					},
					Key: "password",
				},
				NameFromSecret: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "my-db-name-secret",
					},
					Key: "name",
				},
				SSLFromSecret: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "my-db-ssl-secret",
					},
					Key: "ssl",
				},
				MaxConnFromSecret: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "my-db-maxconn-secret",
					},
					Key: "maxconn",
				},
			},
		},
	}

	items := []odoov1.OdooDeployment{
		odooDeployment,
	}

	odooDeploymentList := odoov1.OdooDeploymentList{
		Items: items,
	}

	It("Not using a secret", func() {
		secret := corev1.Secret{}
		secret.Name = "another-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(len(req)).To(Equal(0))
	})
	It("Using a Secret for the Database Host", func() {
		secret := corev1.Secret{}
		secret.Name = "my-db-host-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(len(req)).To(Equal(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
	It("Using a Secret for the Database Port", func() {
		secret := corev1.Secret{}
		secret.Name = "my-db-port-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(len(req)).To(Equal(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
	It("Using a Secret for the Database User", func() {
		secret := corev1.Secret{}
		secret.Name = "my-db-user-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(len(req)).To(Equal(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
	It("Using a secret for the database password", func() {
		secret := corev1.Secret{}
		secret.Name = "my-db-password-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(len(req)).To(Equal(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
	It("Using a Secret for the Database Name", func() {
		secret := corev1.Secret{}
		secret.Name = "my-db-name-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(len(req)).To(Equal(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
	It("Using a Secret for the Database SSL", func() {
		secret := corev1.Secret{}
		secret.Name = "my-db-ssl-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(len(req)).To(Equal(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
	It("Using a Secret for the Database MaxConn", func() {
		secret := corev1.Secret{}
		secret.Name = "my-db-maxconn-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(len(req)).To(Equal(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
})

var _ = Describe("Filter OdooDeployment by Deployment", func() {
	odooDeployment := odoov1.OdooDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-odoo-deployment",
			Namespace: "default",
		},
		Spec: odoov1.OdooDeploymentSpec{
			Name: "my-odoo-deployment",
		},
	}
	items := []odoov1.OdooDeployment{
		odooDeployment,
	}
	odooDeploymentList := odoov1.OdooDeploymentList{
		Items: items,
	}

	It("Not using a Deployment", func() {
		deployment := appsv1.Deployment{}
		deployment.Name = "another-deployment"
		req := filterOdooDeploymentsUsingDeployment(odooDeploymentList, &deployment)
		Expect(len(req)).To(Equal(0))
	})

	It("Using a Deployment", func() {
		deployment := appsv1.Deployment{}
		deployment.Name = "my-odoo-deployment"
		req := filterOdooDeploymentsUsingDeployment(odooDeploymentList, &deployment)
		Expect(len(req)).To(Equal(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
})

var _ = Describe("Filter OdooDeployment by Service", func() {
	odooDeployment := odoov1.OdooDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-odoo-deployment",
			Namespace: "default",
		},
		Spec: odoov1.OdooDeploymentSpec{
			Name: "my-odoo-deployment",
		},
	}
	items := []odoov1.OdooDeployment{
		odooDeployment,
	}
	odooDeploymentList := odoov1.OdooDeploymentList{
		Items: items,
	}

	It("Not using a Service", func() {
		service := corev1.Service{}
		service.Name = "another-service"
		req := filterOdooDeploymentsUsingService(odooDeploymentList, &service)
		Expect(len(req)).To(Equal(0))
	})

	It("Using an HTTP Service", func() {
		service := corev1.Service{}
		service.Name = "my-odoo-deployment-http"
		req := filterOdooDeploymentsUsingService(odooDeploymentList, &service)
		Expect(len(req)).To(Equal(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})

	It("Using a POLL Service", func() {
		service := corev1.Service{}
		service.Name = "my-odoo-deployment-poll"
		req := filterOdooDeploymentsUsingService(odooDeploymentList, &service)
		Expect(len(req)).To(Equal(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
})
