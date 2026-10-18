# Monitoring and alerts

The manager serves `/metrics` (by default on `:8443`, HTTPS, with Kubernetes
authentication and authorization; `--metrics-bind-address=:8080
--metrics-secure=false` serves plain HTTP instead). Besides the
controller-runtime defaults (`controller_runtime_reconcile_*`, `workqueue_*`,
`rest_client_*`, Go and process metrics) it exports the series in the [metrics reference](../reference/metrics.md).

## Alerts

[`config/prometheus/backup-alerts.yaml`](https://github.com/MohanadAbugharbia/odoo-operator/blob/main/config/prometheus/backup-alerts.yaml) holds example alerts on these metrics
(stale or failed backups, failed copies, restores and drills, stores that are
not ready or fail their check or prune). Adjust the staleness thresholds to
your schedules.

## Dashboard

A ready-made Grafana dashboard for these metrics is described in [Grafana
dashboard](grafana-dashboard.md).
