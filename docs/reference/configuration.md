# Configuration

The operator renders `odoo.conf` from `spec.config` and `spec.database`; every field is in the [API reference](api.md).

## Container command

`spec.odooCommand` (default `["odoo"]`) plus `-c /opt/odoo/odoo.conf` becomes
the container `command`, which **replaces the image ENTRYPOINT**. Images whose
entrypoint script injects database flags (e.g. the official image's
`entrypoint.sh`) are bypassed on purpose: the operator's rendered `odoo.conf`
is the only configuration source. The image's own `addons_path` and
`server_wide_modules` must therefore be re-expressed in
`spec.config.extraAddonsPaths` and `spec.config.serverWideModules`.
