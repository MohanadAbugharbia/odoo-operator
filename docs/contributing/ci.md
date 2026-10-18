# Continuous integration

`.github/workflows/ci.yml` runs on every pull request and on pushes to `main`,
in these jobs:

- **Unit and envtest** — `make test` (the envtest suites against a real API
  server), a check that the generated files in `api/v1/zz_generated.deepcopy.go`,
  `config/crd` and `config/rbac` are in sync with the types, and a
  `make build-installer` of the release bundle.
- **Backup on PostgreSQL 16–18** — `hack/backup-it.sh` once per major.
- **Backup e2e (kind)** — `hack/backup-e2e.sh`.
- **Lint** — `make lint` (golangci-lint).
- **Images** — on pull requests from this repository, pushes the operator and
  backup images (amd64 and arm64) tagged `sha-<head sha>` and `pr-<number>`, so
  a branch can be tried on a real cluster before it is released.

`.github/workflows/docs.yml` builds this site on every pull request and
publishes it; see [Writing the docs](docs.md).

## Test results

The test job runs `go test` with `-json` and renders the event stream into a
Markdown report with `hack/test-summary.py`. The report goes to the job summary
on every run and, on pull requests, into a single sticky comment that is
rewritten in place each time (header `go-test-results`), so a PR always shows
the state of its latest run rather than a trail of comments. The full stream and
the coverage profile (plus a rendered `coverage.html`) are uploaded as artifacts
for seven days, and the report links to both.

Two details of the count are worth knowing when reading it:

- **Ginkgo suites are counted in specs, not in Go tests.** `internal/controller`
  and `internal/controller/reconcileloops` each expose one Go test to the
  toolchain (`TestControllers`) that carries the whole suite, so counting the
  stream as-is would report 2 tests for ~70 specs. Where a package's output
  carries Ginkgo's own `Ran N of M Specs` summary, those numbers are used and
  the wrapper test is not counted.
- **Only leaf tests count.** A table-driven parent with subtests contributes its
  subtests, not itself.

A package that fails without a failing test — a build error, a panic outside a
test, a `TestMain` failure — is reported as a failure with its output, not
silently dropped: `go test -json` reports a build failure as a separate event
shape that names no package.

To get the same report locally:

```sh
make test-summary   # writes go-test.json, cover.out and test-summary.md
```
