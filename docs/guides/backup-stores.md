# Set up a backup store

An `OdooBackupStore` says where backups go. Every store holds encrypted
[restic](https://restic.net/) repositories, one per instance under
`<path>/<namespace>/<deployment>/`, encrypted with the key in
`encryptionKeySecret`.

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

## Probes

The operator probes each store with a short Job that writes, reads and deletes
a file (`.odoo-operator-probe-<random>`), on every change to the store or its
Secrets and every six hours. `kubectl get odoobackupstore` shows `Ready`; a
store that fails says why in its `Ready` condition and in an event, and is
probed again every two minutes until it passes. The last failed probe's Job is
kept for its log.

To keep a store healthy over time, [check and prune it](store-check-prune.md)
on a schedule.
