# Restore drills

A drill proves the newest backup can be restored, without
touching the instance: on `spec.backup.drill.schedule` it waits in the
instance's queue, then one Job restores the newest finished backup into a
scratch database `<db>_drill_<suffix>` and a scratch filestore on an
`emptyDir` (never the data volume), runs the same checks as a restore, has the
backup's own Odoo image read it (`odoo shell -d <db>_drill_<suffix>`, run
through `sh`: Odoo loads its registry, reads records of core models and reads
attachments from the scratch filestore), and drops the scratch database. Odoo
keeps serving throughout. The outcome is in `status.backup.lastDrill`
(`lastSuccessfulDrillTime` keeps the last pass); a failed drill keeps its Job
(`kubectl logs job/<name> --all-containers`). The scratch filestore needs node
disk the size of the filestore; `drill.scratchSizeLimit` caps it.
