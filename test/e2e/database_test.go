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

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/MohanadAbugharbia/odoo-operator/test/utils"
)

// Manual scenarios (kind): they need the operator deployed by the "controller"
// spec above, an odoo:18 image pull and a couple of minutes for `-i base`.
//
//	make test-e2e
const e2eNamespace = "odoo-e2e"

func kubectl(args ...string) (string, error) {
	out, err := utils.Run(exec.Command("kubectl", args...))
	return string(out), err
}

// psql runs a query in the e2e PostgreSQL pod and returns the trimmed output.
func psql(query string) (string, error) {
	out, err := kubectl("-n", e2eNamespace, "exec", "deploy/e2e-postgres", "--",
		"psql", "-U", "odoo", "-d", "postgres", "-tA", "-c", query)
	return strings.TrimSpace(out), err
}

func databaseExists(name string) bool {
	out, err := psql(fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = '%s'", name))
	return err == nil && out == "1"
}

func statusField(name, jsonPath string) string {
	out, _ := kubectl("-n", e2eNamespace, "get", "odoodeployment", name, "-o", "jsonpath={"+jsonPath+"}")
	return strings.TrimSpace(out)
}

// databaseLifecycleContext holds the database scenarios; it is registered
// inside the Ordered "controller" container so it always runs after the
// operator has been deployed.
func databaseLifecycleContext() {
	Context("database lifecycle", Ordered, func() {
		BeforeAll(func() {
			By("creating the e2e namespace and PostgreSQL")
			_, _ = kubectl("create", "ns", e2eNamespace)
			_, err := kubectl("apply", "-f", "test/e2e/testdata/postgres.yaml")
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() error {
				_, err := psql("SELECT 1")
				return err
			}, 3*time.Minute, 5*time.Second).Should(Succeed())
		})

		AfterAll(func() {
			for _, f := range []string{"odoodeployment-operator-db.yaml", "odoodeployment-external-db.yaml"} {
				_, _ = kubectl("delete", "-f", "test/e2e/testdata/"+f, "--ignore-not-found", "--wait=false")
			}
			_, _ = kubectl("delete", "ns", e2eNamespace, "--wait=false")
		})

		It("creates, tags, runs on and finally drops an operator-provisioned database", func() {
			_, err := kubectl("apply", "-f", "test/e2e/testdata/odoodeployment-operator-db.yaml")
			Expect(err).NotTo(HaveOccurred())

			By("the database is created and tagged")
			Eventually(func() string { return statusField("e2e-operator-db", ".status.database.provisionedBy") },
				2*time.Minute, 5*time.Second).Should(Equal("operator"))
			Expect(databaseExists("e2e_created")).To(BeTrue())
			comment, err := psql("SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = 'e2e_created'")
			Expect(err).NotTo(HaveOccurred())
			Expect(comment).To(Equal("odoo-operator:" + e2eNamespace + "/e2e-operator-db"))

			By("the init job installs base and the deployment comes up")
			Eventually(func() string { return statusField("e2e-operator-db", ".status.phase") },
				15*time.Minute, 10*time.Second).Should(Equal("Running"))
			Eventually(func() string {
				return statusField("e2e-operator-db", `.status.conditions[?(@.type=="Ready")].status`)
			}, 10*time.Minute, 10*time.Second).Should(Equal("True"))

			By("deleting the CR drops the database")
			_, err = kubectl("delete", "-f", "test/e2e/testdata/odoodeployment-operator-db.yaml", "--wait=true", "--timeout=5m")
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() bool { return databaseExists("e2e_created") }, 2*time.Minute, 5*time.Second).Should(BeFalse())
		})

		It("adopts a pre-existing database as external and never drops it", func() {
			_, err := psql("CREATE DATABASE keepme")
			Expect(err).NotTo(HaveOccurred())

			_, err = kubectl("apply", "-f", "test/e2e/testdata/odoodeployment-external-db.yaml")
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() string { return statusField("e2e-external-db", ".status.database.provisionedBy") },
				2*time.Minute, 5*time.Second).Should(Equal("external"))
			finalizers, _ := kubectl("-n", e2eNamespace, "get", "odoodeployment", "e2e-external-db",
				"-o", "jsonpath={.metadata.finalizers}")
			Expect(finalizers).NotTo(ContainSubstring("odoo.abugharbia.com/database"))

			_, err = kubectl("delete", "-f", "test/e2e/testdata/odoodeployment-external-db.yaml", "--wait=true", "--timeout=5m")
			Expect(err).NotTo(HaveOccurred())
			Consistently(func() bool { return databaseExists("keepme") }, 30*time.Second, 5*time.Second).Should(BeTrue())
		})
	})
}
