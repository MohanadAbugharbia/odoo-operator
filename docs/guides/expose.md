# Expose Odoo

Every instance gets two Services, named after its `metadata.name`:

| Service | Port | Serves |
|---|---|---|
| `<name>-http` | 8069 | Odoo's web interface and RPC |
| `<name>-poll` | 8072 | Odoo's live-update channel (`/websocket`; `/longpolling` before Odoo 16), when Odoo runs with workers |

Both exist from the first reconcile, before the database and the init Job, so
an Ingress can be applied together with the `OdooDeployment`. They have no
endpoints until Odoo's pods are ready, unless the [maintenance
page](maintenance-page.md) is on: then `<name>-http` points at it while Odoo
is installed, upgraded or started. `<name>-poll` always points at Odoo.

## Route the live-update channel

With `spec.config.workers` above 0 (the default is 2), Odoo serves live
updates (chat, notifications, the POS) from a separate process on port 8072.
Send `/websocket` there and everything else to port 8069. With
[ingress-nginx](https://kubernetes.github.io/ingress-nginx/) (the annotations
are its own; other controllers have equivalents):

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: shop
  namespace: shop
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: 100m     # uploads and imports
    nginx.ingress.kubernetes.io/proxy-read-timeout: "720"  # long requests and the websocket
spec:
  ingressClassName: nginx
  tls:
    - hosts: [odoo.example.com]
      secretName: odoo-example-com-tls
  rules:
    - host: odoo.example.com
      http:
        paths:
          - path: /websocket
            pathType: Prefix
            backend:
              service: {name: my-odoo-poll, port: {number: 8072}}
          - path: /
            pathType: Prefix
            backend:
              service: {name: my-odoo-http, port: {number: 8069}}
```

With `workers: 0` Odoo runs threaded and serves everything, live updates
included, on port 8069: the `/websocket` rule is then not needed.

## Proxy mode

`spec.config.proxyMode` is `true` by default: Odoo then trusts the
`X-Forwarded-For`, `X-Forwarded-Host` and `X-Forwarded-Proto` headers for the
client address, host and scheme (Odoo applies them only when the proxy also
sends `X-Forwarded-Host`; ingress-nginx does). Behind an Ingress controller
that sets them, keep it on; reaching Odoo directly, without a proxy that sets them, turn it
off.
