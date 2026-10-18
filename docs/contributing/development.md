# Development

## Prerequisites

- go version v1.25.0+
- docker version 17.03+.
- kubectl version v1.28.0+.
- Access to a Kubernetes v1.28.0+ cluster.

## Running the tests

```sh
make test
```

This runs the unit suites and the envtest ones (the Kubernetes binaries are
downloaded on demand). Coverage is written to `cover.out` and measured with
`-coverpkg` across every shipped package, so a helper exercised by another
package's suite is credited to it; measuring per-package instead understated
the project by roughly 25 points.

The `internal/database` tests that need a real PostgreSQL skip unless one is
configured, because they assert the SQL the provisioner emits, the `COMMENT`
that marks operator-owned databases and the refusal to drop a database
carrying somebody else's comment. CI starts a `postgres:16` service for them.
To run them locally against any throwaway server:

```sh
TEST_PG_HOST=127.0.0.1 TEST_PG_PORT=5432 \
TEST_PG_USER=postgres TEST_PG_PASSWORD=postgres make test
```

The backup agent's real-service tests (`internal/backup/agent`) skip without
`TEST_BACKUP_PG_HOST` (the same variables with a `BACKUP_` infix), because
they also need restic, rclone and the client tools of the server's major.
`hack/backup-it.sh` runs them inside the backup image against PostgreSQL, an
SFTP server and an S3 server it starts in Docker: fidelity of a round trip,
the repair from the previous backup, undo, cleanup, and a failure or crash
injected at every step of a restore, which must then either leave the live
data untouched or finish when run again.

```sh
PG_MAJOR=17 hack/backup-it.sh
```

`hack/backup-e2e.sh` backs up, restores and undoes a real Odoo in a kind
cluster through an S3 store and a PVC store. CI runs both.

`make test-e2e` additionally needs Docker and kind, and is not part of CI.

## Deploy a build on a cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/odoo-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/odoo-operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

## Uninstall it
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

The operator is built with [Kubebuilder](https://book.kubebuilder.io/introduction.html); its book explains the project layout and the `make` targets.
