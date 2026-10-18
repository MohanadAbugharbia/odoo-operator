# Monitoring and alerts

The manager serves `/metrics` on `:8443` over HTTPS, with Kubernetes
authentication and authorization, through the
`odoo-operator-controller-manager-metrics-service` Service. That port comes
from the install bundle (`--metrics-bind-address=:8443`); the binary alone
serves no metrics. `--metrics-bind-address=:8080 --metrics-secure=false`
serves plain HTTP instead. Besides the controller-runtime defaults
(`controller_runtime_reconcile_*`, `workqueue_*`, `rest_client_*`, Go and
process metrics) it exports the series in the [metrics reference](../reference/metrics.md).

## Scrape it

The install bundle does not include a ServiceMonitor. With the Prometheus
Operator, this one scrapes the HTTPS endpoint:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: odoo-operator
  namespace: odoo-operator-system
spec:
  selector:
    matchLabels:
      control-plane: controller-manager
  endpoints:
    - port: https
      path: /metrics
      scheme: https
      bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
      tlsConfig:
        insecureSkipVerify: true   # the manager's certificate is self-signed
      honorLabels: true
```

- **`honorLabels: true` is required.** The per-instance series carry the
  namespace of their OdooDeployment in `namespace`; without it Prometheus
  renames that label to `exported_namespace`, and the alerts and the
  [dashboard](grafana-dashboard.md) lose it.
- **Prometheus needs read access to `/metrics`.** Bind its service account to
  the `odoo-operator-metrics-reader` ClusterRole, which the bundle installs.
- [`config/prometheus/`](https://github.com/MohanadAbugharbia/odoo-operator/tree/main/config/prometheus)
  holds the same ServiceMonitor and the example alerts as a kustomize base. It
  is not part of the install bundle.

## Alerts

[`config/prometheus/backup-alerts.yaml`](https://github.com/MohanadAbugharbia/odoo-operator/blob/main/config/prometheus/backup-alerts.yaml) holds example alerts on these metrics
(stale or failed backups, failed copies, restores and drills, stores that are
not ready or fail their check or prune). Adjust the staleness thresholds to
your schedules.

## Dashboard

A ready-made Grafana dashboard for these metrics is described in [Grafana
dashboard](grafana-dashboard.md).
