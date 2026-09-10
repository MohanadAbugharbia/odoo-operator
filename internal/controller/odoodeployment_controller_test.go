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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

var _ = Describe("ExtraAddonsPaths CRD validation", func() {
	makeResource := func(name string, paths []string) *odoov1.OdooDeployment {
		return &odoov1.OdooDeployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
			},
			Spec: odoov1.OdooDeploymentSpec{
				Name:  name,
				Image: "odoo:18",
				Database: odoov1.OdooDatabaseConfig{
					Host: "db-host",
					Port: 5432,
					User: "odoo",
					PasswordFromSecret: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "db-secret"},
						Key:                  "password",
					},
				},
				Config:  odoov1.OdooConfig{ExtraAddonsPaths: paths},
				Modules: []string{"base"},
			},
		}
	}

	DescribeTable("admission validation",
		func(name string, paths []string, expectAccepted bool) {
			resource := makeResource(name, paths)
			err := k8sClient.Create(ctx, resource)
			if expectAccepted {
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() {
					Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				})
			} else {
				Expect(err).To(HaveOccurred())
				Expect(errors.IsInvalid(err)).To(BeTrue())
			}
		},
		Entry("valid absolute path is accepted", "addons-valid-1", []string{"/mnt/extra-addons"}, true),
		Entry("multiple valid paths are accepted", "addons-valid-2", []string{"/mnt/addons-a", "/mnt/addons-b"}, true),
		Entry("relative path is rejected", "addons-invalid-1", []string{"extra-addons"}, false),
		Entry("path with comma is rejected", "addons-invalid-2", []string{"/mnt/ex,tra"}, false),
		Entry("path with hash is rejected", "addons-invalid-3", []string{"/mnt/add#ons"}, false),
		Entry("path with space is rejected", "addons-invalid-4", []string{"/mnt/extra addons"}, false),
		Entry("path with newline is rejected", "addons-invalid-5", []string{"/mnt/add\nons"}, false),
		Entry("duplicate paths are rejected", "addons-invalid-6", []string{"/mnt/a", "/mnt/a"}, false),
	)
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
		Expect(req).To(BeEmpty())
	})

	It("Using a PVC", func() {
		pvc := corev1.PersistentVolumeClaim{}
		pvc.Name = "my-pvc-name"
		req := filterOdooDeploymentsUsingPVC(odooDeploymentList, &pvc)
		Expect(req).To(HaveLen(1))
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
		Expect(req).To(BeEmpty())
	})
	It("Using a Secret for the Database Host", func() {
		secret := corev1.Secret{}
		secret.Name = "my-db-host-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(req).To(HaveLen(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
	It("Using a Secret for the Database Port", func() {
		secret := corev1.Secret{}
		secret.Name = "my-db-port-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(req).To(HaveLen(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
	It("Using a Secret for the Database User", func() {
		secret := corev1.Secret{}
		secret.Name = "my-db-user-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(req).To(HaveLen(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
	It("Using a secret for the database password", func() {
		secret := corev1.Secret{}
		secret.Name = "my-db-password-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(req).To(HaveLen(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
	It("Using a Secret for the Database Name", func() {
		secret := corev1.Secret{}
		secret.Name = "my-db-name-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(req).To(HaveLen(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
	It("Using a Secret for the Database SSL", func() {
		secret := corev1.Secret{}
		secret.Name = "my-db-ssl-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(req).To(HaveLen(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
	It("Using a Secret for the Database MaxConn", func() {
		secret := corev1.Secret{}
		secret.Name = "my-db-maxconn-secret"
		req := filterOdooDeploymentsUsingSecret(odooDeploymentList, &secret)
		Expect(req).To(HaveLen(1))
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
		Expect(req).To(BeEmpty())
	})

	It("Using a Deployment", func() {
		deployment := appsv1.Deployment{}
		deployment.Name = "my-odoo-deployment"
		req := filterOdooDeploymentsUsingDeployment(odooDeploymentList, &deployment)
		Expect(req).To(HaveLen(1))
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
		Expect(req).To(BeEmpty())
	})

	It("Using an HTTP Service", func() {
		service := corev1.Service{}
		service.Name = "my-odoo-deployment-http"
		req := filterOdooDeploymentsUsingService(odooDeploymentList, &service)
		Expect(req).To(HaveLen(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})

	It("Using a POLL Service", func() {
		service := corev1.Service{}
		service.Name = "my-odoo-deployment-poll"
		req := filterOdooDeploymentsUsingService(odooDeploymentList, &service)
		Expect(req).To(HaveLen(1))
		Expect(req[0].Name).To(Equal("my-odoo-deployment"))
	})
})
