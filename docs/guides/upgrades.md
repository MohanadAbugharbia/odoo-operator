# Upgrade Odoo

An upgrade runs Odoo's `-u` on the database in a Job, with the Deployment scaled to zero, whenever the image changes or you ask for it.

- `spec.upgrade.onImageChange: true` (default): every `spec.image` change runs
  an upgrade Job with `-u <spec.upgrade.modules>` before the Deployment rolls
  to the new image. New entries in `spec.modules` are installed in the same Job.
- Empty `spec.upgrade.modules` rolls the new image without a Job.
- **A new image is pulled before anything stops.** When `spec.image` changes on
  a running instance, a short `<name>-image-<hash>` Job runs
  `<odooCommand> --version` on it first, without the filestore or the config.
  Only once it succeeds is the Deployment scaled down (or rolled). Until then
  the old image keeps serving; if the image cannot be pulled `Degraded` is
  `ImagePullFailed` (or `ImageCheckFailed` when it pulled but did not run), and
  a later `spec.image` replaces the check. This is what keeps an instance up
  when its image tag is published before the image is pushed, or never is.
- An init or upgrade Job whose pod cannot pull its image is reported as
  `ImagePullFailed`. If `spec.image` changes meanwhile, the Job is replaced
  by one for the new image: its container never started, so nothing touched
  the database.
- For production set `onImageChange: false` and bump `spec.upgrade.token` to
  run the upgrade on demand.
- `spec.jobs` bounds the Jobs (`activeDeadlineSeconds`, `ttlSecondsAfterFinished`,
  `backoffLimit`).

A failed upgrade can be rolled back by [restoring a backup](restore.md): a restore replaces a failed upgrade Job.
