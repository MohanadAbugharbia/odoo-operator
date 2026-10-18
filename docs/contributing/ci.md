# Continuous integration

`.github/workflows/ci.yml` runs on pull requests and on pushes to `main`. Its
first job, **Changes**, decides which of the others run, so a pull request
pays only for the tests its change needs:

| Job | What it runs | Runs on a pull request | Runs on `main` |
|---|---|---|---|
| **Unit and envtest** | `make test` (the envtest suites against a real API server), a check that `api/v1/zz_generated.deepcopy.go`, `config/crd` and `config/rbac` are in sync with the types, and a `make build-installer` of the release bundle | when anything but docs changed | when anything but docs changed |
| **Lint** | `make lint` (golangci-lint) | when anything but docs changed | when anything but docs changed |
| **Backup on PostgreSQL 16–18** | `hack/backup-it.sh` once per major | when backup code changed, or with `full-ci` or `backup-ci` | when anything but docs changed |
| **Backup e2e (kind)** | `hack/backup-e2e.sh` | when backup code changed, or with `full-ci` or `backup-ci` | when anything but docs changed |
| **Images** | pushes the operator and backup images (amd64 and arm64) tagged `sha-<head sha>` and `pr-<number>`, to try a branch on a real cluster | with `full-ci` or `images-ci`, and only from this repository | never |

- **Docs** means `docs/`, Markdown files, `mkdocs.yml`, `LICENSE` and the Docs
  workflow. A docs-only change skips every job here.
- **Backup code** is the list in the `backup` filter of the Changes job: the
  backup agent and its image, the backup and restore types, CRDs and RBAC,
  the backup controllers and the instance work queue, schedules, `go.mod` and
  `go.sum`, and `ci.yml` itself. A backup-related change outside that list is
  still caught by the full run on `main`; add the path to the filter when that
  happens.
- **Labels.** `full-ci` runs everything, `backup-ci` the backup suite and
  `images-ci` the images. Adding one of them runs CI again with it, and the
  newer run replaces the one in progress. Other labels do not start CI.
- A skipped job counts as passed, so these jobs can be required checks.
- The Changes job's summary says what ran and why.

`.github/workflows/docs.yml` builds this site on every pull request, and
publishes it from `main`, from releases and, as a preview, from pull requests
with the `docs-preview` label; see [Writing the docs](docs.md).

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
