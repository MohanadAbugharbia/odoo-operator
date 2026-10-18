# Take and schedule backups

Backups need three objects: a store, the instance pointing at it, and a
backup.

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
---
# in the OdooDeployment
spec:
  backup:
    stores: [offsite]
    consistency: Online               # default; Offline stops Odoo while it runs
---
apiVersion: odoo.abugharbia.com/v1
kind: OdooBackup
metadata:
  name: my-odoo-2026-10-17
  namespace: shop
spec:
  odooDeployment: my-odoo
```

With a [store](backup-stores.md) set up (check that it is `Ready`: a backup
does not wait for that), creating an `OdooBackup` takes a backup; [How backups work](../concepts/backups.md) explains what it holds and
the phases it goes through.

## Schedules and retention

`spec.backup.schedule` takes backups on a schedule, and `retention` decides
which old ones are deleted after each backup:

```yaml
spec:
  backup:
    stores: [offsite, cold]           # the first is written; the others get copies
    schedule: "0 3 * * *"             # cron or @daily; empty (default) = no scheduled backups
    timeZone: Europe/Berlin           # of the schedules and the retention periods (default UTC)
    retention:                        # defaults: daily 7, weekly 4, monthly 12
      daily: 7
      weekly: 4
      monthly: 12
      within: 2d                      # also keep everything from the last 2 days (y, m, d, h)
      manual: 10                      # newest manual backups kept; 0 (default) keeps all
    drill:
      schedule: "0 5 * * 0"           # [restore drill](drills.md)
```

A scheduled backup is an ordinary OdooBackup named `<instance>-<yyyymmdd-hhmm>`
(the time in `timeZone`) that waits in the instance's queue like any other. A
time missed while the operator was down is taken once when it is back; a time
that comes while the previous scheduled backup is still unfinished is skipped
with an event. `status.backup` shows the last and next scheduled times, and the
`BackupSchedule` condition reports a schedule that cannot be read.

Retention runs in the backup Job, after the new backup is verified. The keep
rules are restic's (`last`, `hourly`, `daily`, `weekly`, `monthly`, `yearly`,
`within`, with restic's meaning: `within` counts back from the newest backup,
not from now), but the periods are counted in `timeZone`, and they apply to
scheduled backups; manual backups keep their newest `manual`. Never deleted: the newest backup, a backup with
`spec.keep: true` (it does not use up a slot of the rules either), a snapshot
an unfinished restore needs, and snapshots the operator did not take. The
OdooBackups of deleted snapshots are deleted too; a retention that fails leaves
the backup `Completed` with a warning. `retention.keepAll: true` turns it off.
One run deletes at most 24 snapshots, oldest first, so a tightened policy
takes a few backups to catch up. Deleting the snapshot frees no space by
itself: the store's [prune](store-check-prune.md) does.

A failed backup is not retried: the next scheduled one, or a new OdooBackup,
is the retry. Its Job is kept for `spec.jobs.ttlSecondsAfterFinished` (24
hours by default) for its log.

## Copies

Each backup is copied (`restic copy`) to every store after the
first in `spec.backup.stores`, in the same Job, and retention runs in each
copy too. A store that is missing or whose Secret is incomplete is skipped; a
store that is not ready is still tried. A skipped store or a copy that fails leaves
the backup `PartiallyCompleted` with `status.copies` saying why; the backup in
the first store is still good. A restore (or drill) whose backup's store is
not ready restores a copy from a ready store instead, with an event saying so.
Stores may use different kinds and different encryption keys.
