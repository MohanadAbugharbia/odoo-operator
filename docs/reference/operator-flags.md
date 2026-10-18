# Operator flags

The manager (`odoo-operator`, the `manager` container of
`odoo-operator-controller-manager`) takes these flags. The install bundle
sets `--metrics-bind-address=:8443`, `--leader-elect` and
`--health-probe-bind-address=:8081`.

| Flag | Default | Effect |
|---|---|---|
| `--metrics-bind-address` | `0` | Where `/metrics` listens: `:8443` for HTTPS, `:8080` for HTTP, `0` to turn it off. See [Monitoring and alerts](../guides/monitoring.md). |
| `--metrics-secure` | `true` | Serve metrics over HTTPS, with Kubernetes authentication and authorization. `--metrics-secure=false` serves plain HTTP. |
| `--health-probe-bind-address` | `:8081` | Where the liveness and readiness probes listen. |
| `--leader-elect` | `false` | Elect one active manager among the replicas. Only the leader reconciles and reports the scrape-time metrics. |
| `--enable-http2` | `false` | Allow HTTP/2 on the metrics server; off by default because of the HTTP/2 stream cancellation and rapid reset CVEs. |
| `--log-level` | `info` | `debug`, `info`, `warn` or `error`. |
| `--maintenance-page-image` | the operator's own image | Image of the [maintenance page](../guides/maintenance-page.md) pod. By default the operator reads its own image from its pod. |
| `--backup-image` | the operator's repository with `-backup` appended, at the same tag | Image of the backup, restore, drill and store Jobs. Set it when you mirror images: for example to `registry.example.com/odoo-operator-backup:0.6.0`. |

!!! note "Mirroring images"
    An air-gapped or mirrored install needs both images: the operator's and
    `ghcr.io/mohanadabugharbia/odoo-operator-backup` at the same tag. When
    the operator's image is pinned by digest only, the derived backup image
    falls back to the `latest` tag; set `--backup-image` explicitly then.
