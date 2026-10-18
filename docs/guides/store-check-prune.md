# Check and prune a store

On their own schedules a store's repositories
are checked (`restic check`) and pruned (`restic prune`, which frees the space
of deleted backups):

```yaml
spec:                                 # OdooBackupStore
  check:
    schedule: "0 4 * * 0"
    readDataParts: 7                  # each check reads 1/7 of the data, all of it over 7 checks;
                                      # 1 = everything every time, 0 = structure only
  prune:
    schedule: "0 2 * * 6"
  timeZone: Europe/Berlin
```

Checks and prunes only run while the store is `Ready`. One Job runs at a
time per store, prune first when both are due, over the
repositories of every instance in the namespace that lists the store. The
outcome is in `status.lastCheck` and `status.lastPrune` (with the part read,
the repositories and what they hold); a failed task keeps its Job for its log
and warns. A backup that starts while a prune holds a repository's lock waits
up to half an hour for it.
