# Set up a backup store

An `OdooBackupStore` says where backups go. Every store holds encrypted
[restic](https://restic.net/) repositories, one per instance under
`<path>/<namespace>/<deployment>/`, encrypted with the key in
`encryptionKeySecret`. A [read-only store](#read-only-stores) is where
backups are only read from, such as Odoo backup files to start an instance
from. A [zip store](#zip-stores) takes copies of backups as Odoo's own zip
files.

!!! warning "Keep the encryption key"
    Keep a copy of the key in `encryptionKeySecret` outside the cluster:
    without it no backup can be read.

```yaml
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackupStore
metadata:
  name: offsite
  namespace: shop
spec:
  s3:                                 # or sftp, nfs, pvc: exactly one
    endpoint: https://s3.example.com
    bucket: odoo-backups
    path: production
    credentialsSecret: { name: s3-credentials }   # AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
  encryptionKeySecret: { name: backup-key, key: password }
```

## Kinds of store

| Kind | Fields | Secret |
|---|---|---|
| `s3` | `endpoint` (empty for AWS), `region`, `bucket`, `path`, `forcePathStyle` | `credentialsSecret`: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` |
| `sftp` | `host`, `port` (22), `user`, `path` | `sshSecret`: the private key under `id_ed25519` or `ssh-privatekey`, and `known_hosts` (the server's host key; nothing else is trusted) |
| `nfs` | `server`, `path` | |
| `pvc` | `claimName`, `subPath` | |

## SFTP keys

The SFTP private key can be any unencrypted Ed25519, ECDSA or RSA key, in
OpenSSH or PEM format; a key with a passphrase does not work. The key name
only says where the key is, not its type: `id_ed25519` is read first, and
`ssh-privatekey` is the key of Secrets of type `kubernetes.io/ssh-auth`.
`known_hosts` takes the server's host key of any type:

```sh
ssh-keyscan -p 22 backup.example.com > known_hosts
kubectl -n shop create secret generic my-odoo-sftp \
  --from-file=id_ed25519=$HOME/.ssh/backup_key --from-file=known_hosts
```

Check the scanned key against the one the server's provider shows before you
trust it.

## Read-only stores

`readOnly: true` makes a store the operator only reads: restores and
[bootstraps](start-from-a-backup.md) take backups from it with `from`, and
nothing is ever written to it. Use it for the backups of another instance or
cluster, or for a folder or bucket of Odoo backup files:

```yaml
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackupStore
metadata:
  name: imports
  namespace: shop
spec:
  s3:
    endpoint: https://s3.example.com
    bucket: odoo-exports
    credentialsSecret: { name: s3-readonly-credentials }
  readOnly: true
```

- Its probe only lists the store, so read-only credentials are enough.
- Restic reads it without locks (`--no-lock`), which works on read-only
  credentials and never blocks the instance that writes the repository.
- `encryptionKeySecret` is optional: a store without one holds only backup
  files (`.zip` and `.dump`), and a restore of a restic repository from it is
  refused.
- It cannot be in `spec.backup.stores` (backups to it fail), and it is never
  [checked or pruned](store-check-prune.md).

## Zip stores

`format: zip` makes a store that keeps each backup as the zip Odoo's database
manager makes (`dump.sql`, `filestore/`, `manifest.json`), which any Odoo
loads with its database manager or `odoo db load`. Put it after the first
store in `spec.backup.stores` and every backup is [copied](backups.md#copies)
into it as a zip:

```yaml
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackupStore
metadata:
  name: zips
  namespace: shop
spec:
  sftp:
    host: backup.example.com
    user: odoo
    path: /exports
    sshSecret: { name: my-odoo-sftp }
  format: zip
```

- The zips are at `<path>/<namespace>/<deployment>/<database>_<YYYY-MM-DD_HH-MM-SS>.zip`,
  named after the backup's time in UTC, and the backup's `status.copies`
  names its zip.
- A zip is written under a temporary name (`.<name>.partial`) and renamed when
  complete, so the store never shows a zip cut short. An upload whose pod was
  killed leaves its `.<name>.partial` behind (a retry writes a zip of a new
  name); the next copy to the store deletes the instance's leftover
  `.<database>_<YYYY-MM-DD_HH-MM-SS>.zip.partial` files from its folder.
- When retention forgets a backup, its zip is deleted. Only the names the
  instance's own backups get (and their `.partial` upload names) are ever
  deleted: other files in the folder stay.
- A zip holds every file again, so each copy costs the full database and
  filestore; with restic, a backup costs only what changed. Keep the first
  store a restic store and use a zip store for what needs plain Odoo files.
- Zips are not encrypted: the store's own access control is all that guards
  them. `encryptionKeySecret` is not needed.
- A zip store cannot be the first store (backups are taken into restic
  stores only), and it is never [checked or pruned](store-check-prune.md).
  Its `format` cannot change once it is created.
- A restore whose backup's own store is not ready restores the zip instead;
  a [drill](drills.md) only uses restic copies.

## Probes

The operator probes each store with a short Job that writes, reads and deletes
a file (`.odoo-operator-probe-<random>`), on every change to the store or its
Secrets and every six hours. `kubectl get odoobackupstore` shows `Ready`; a
store that fails says why in its `Ready` condition and in an event, and is
probed again every two minutes until it passes. The last failed probe's Job is
kept for its log.

To keep a store healthy over time, [check and prune it](store-check-prune.md)
on a schedule.
