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

## When Odoo restarts

The Deployment uses the `Recreate` strategy, and its pods carry a hash of the
rendered `odoo.conf`. Any change that changes the file restarts Odoo with a
short downtime: a `spec.config` change, but also a new database password in
the connection Secret. The operator owns the Deployment's pod template, so
changes made to it by hand are reverted.

## Master password

Odoo's master password (`admin_passwd`, which guards the database manager) is
read from the Secret `<name>-admin-password`, key `password`, or from the
Secret named in `spec.config.adminPasswordSecretName`. When the key is empty
the operator generates a password once; it never rotates it. A Secret you name
yourself is not deleted with the instance.
