# Maintenance page

![The maintenance page during an upgrade, with the default title and colour](../assets/maintenance-page-light.png#only-light)
![The maintenance page during an upgrade, with the default title and colour](../assets/maintenance-page-dark.png#only-dark)

*An upgrade in progress, with the default branding (title "Odoo", colour `#714b67`). The page follows the browser's light or dark mode.*

With `spec.maintenancePage.enabled: true`, a browser opening the instance
while Odoo cannot answer gets a status page instead of a blank screen:

```yaml
spec:
  maintenancePage:
    enabled: true
    title: My Shop            # default "Odoo"
    titleTranslations:
      ar: متجري
    accentColor: "#f2800d"    # default "#714b67"
    logo: data:image/svg+xml;base64,…   # optional, data URIs only
```

- Whenever Odoo's Deployment has no available replica (first setup, an
  upgrade, a restart) and `spec.replicas` is not 0, the operator runs a
  `<name>-maintenance` Deployment (one pod, 10m CPU / 32Mi) and points the
  `<name>-http` Service's selector at it. Once Odoo has an available replica
  the selector moves back and the page scales to zero. The `<name>-poll`
  Service always stays on Odoo.
- The pod runs the operator's own image (`odoo-operator maintenance-page`), so
  there is nothing extra to build or pin. The operator reads its image from
  its own pod, retrying on each reconcile until it succeeds (a failure shows
  as a `MaintenancePageImageUnknown` warning event on the OdooDeployment);
  `--maintenance-page-image` overrides it.
- The page needs no access to the API server: after every reconcile the
  operator writes what the page shows into the `<name>-maintenance`
  ConfigMap, which the pod mounts (no ServiceAccount token). On each change
  it also stamps the status hash on the page pods
  (`odoo.abugharbia.com/maintenance-status`), which makes the kubelet refresh
  the mount at once instead of on its next periodic sync, up to a minute later.
- It answers like a server that is down, so Odoo's own clients keep their
  offline behaviour: a page navigation (`Sec-Fetch-Mode: navigate`) gets the
  page with status 503, and every other request (RPC, assets, `/web/health`,
  service worker fetches) gets an empty 503. Nothing is cacheable.
- The page shows the phase as steps (preparing the database, installing,
  updating, starting), the elapsed time and, for upgrades, an estimate from
  `status.lastMaintenanceDuration`. It polls `/__maintenance/status.json` and
  `/web/health` every 3 seconds, and reopens the URL that was asked for as soon
  as Odoo answers. A failed Job shows only "Please contact your
  administrator"; the reason stays in the CR's conditions and events.
- English or Arabic (right to left) is picked from the browser's
  `Accept-Language`; it follows the device's light or dark mode, and loads no
  external fonts or scripts.
