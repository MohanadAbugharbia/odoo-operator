# Odoo backup fixtures

Real backups made by Odoo itself, one folder per series (`17.0`, `18.0`,
`19.0`). Each folder holds:

- `fixture.zip` - Odoo's "zip" backup (`dump.sql`, `manifest.json`, `filestore/`).
- `fixture.dump` - Odoo's "dump" backup (a `pg_dump` custom-format archive, no filestore).
- `wrapped.zip` - the same entries as `fixture.zip`, each moved under one extra
  folder `fixture-backup/`, like a downloaded backup that someone re-zipped.

## How they were made

Images (pulled 2026-10-18):

| Series | Image | `odoo --version` | Image digest |
|---|---|---|---|
| 17.0 | `odoo:17` | `Odoo Server 17.0-20260908` | `sha256:8779f8157ddcc37b84014c94646073f46fd4ad776b7a5d95fc3b0dc73a24a8fa` |
| 18.0 | `odoo:18` | `Odoo Server 18.0-20261015` | `sha256:1b346c6f9145c79b802b711a554a141acbd0503ed6cd7d31cb3c938889c4cc36` |
| 19.0 | `odoo:19` | `Odoo Server 19.0-20261015` | `sha256:076562619a5de059388d9f9d90545cb575799c04869610a5b20839893819b41b` |

The database server was `postgres:16` (PostgreSQL 16.15) on a private docker
network. All three Odoo images ship `pg_dump` 18.6, so the dumps come from
`pg_dump` 18.6.

Odoo config file used for every step:

```ini
[options]
admin_passwd = fixture-master
db_host = fixture-pg
db_user = odoo
db_password = odoo
data_dir = /var/lib/odoo
list_db = True
workers = 0
```

Steps, for each series:

1. Create the database with only `base` and no demo data:
   `odoo -c fixture.conf -d fixture -i base --without-demo=all --stop-after-init`
   (Odoo 19 skips demo data by default, so the `--without-demo` flag was left out there).
2. Add one attachment through `odoo shell -c fixture.conf -d fixture`:
   ```python
   env['ir.attachment'].create({'name': 'fixture.txt', 'raw': b'odoo fixture <series>\n'})
   env.cr.commit()
   ```
   `ir_attachment.location` was not set, so Odoo used its default file store.
3. Start the Odoo HTTP server and download the backups the same way the
   database manager does:
   ```sh
   curl -X POST -F master_pwd=fixture-master -F name=fixture -F backup_format=zip  \
        -o fixture.zip  http://localhost:8069/web/database/backup
   curl -X POST -F master_pwd=fixture-master -F name=fixture -F backup_format=dump \
        -o fixture.dump http://localhost:8069/web/database/backup
   ```
4. Build `wrapped.zip` with Python's `zipfile`: copy every entry of `fixture.zip`,
   with `fixture-backup/` in front of its name (same bytes and timestamps, deflate
   compression, no separate directory entries).

The facts below were read from the restored databases and from the files, not
written by hand.

## Things to know

- Installing `base` also installs a few modules Odoo always auto-installs (`web`,
  `bus`, `base_setup` and others; see `modules` in `manifest.json`). Those modules
  write their own files to the filestore. So the filestore holds 11 files, not
  just `fixture.txt`, plus a `filestore/checklist/` tree of the same 11 names
  (Odoo's filestore garbage-collection checklist, empty marker files).
- The zips contain file entries only, no directory entries.
- `fixture.dump` has archive version 1.16 (from `pg_dump` 18). `pg_restore` 16
  cannot read it ("unsupported version (1.16) in file header"). Use `pg_restore`
  17 or newer.
- Both `dump.sql` and `fixture.dump` start with `SET transaction_timeout = 0;`.
  A PostgreSQL 16 server rejects that one line. `psql` keeps going, but
  `pg_restore` reports "errors ignored on restore: 1" and exits with a non-zero
  code even though the data restores fine.
- `dump.sql` starts with a `\restrict <random key>` line (newer `pg_dump`). It
  needs a `psql` that knows that command (16.10 or newer works).
- `ir_attachment` has 14 rows: 13 have a `store_fname` (some share a file), and
  one (`placeholder.png`) is a `url` attachment. None keep their data in the database (`db_datas`).

## Facts per series

| Fact | 17.0 | 18.0 | 19.0 |
|---|---|---|---|
| Attachment name | `fixture.txt` | `fixture.txt` | `fixture.txt` |
| Attachment id | 14 | 14 | 14 |
| Attachment content | `odoo fixture 17.0\n` | `odoo fixture 18.0\n` | `odoo fixture 19.0\n` |
| Attachment size (bytes) | 18 | 18 | 18 |
| Attachment sha1 (`checksum`, file name) | `21c14c1c5f4d89a998aa5b477f781aadfd8856ad` | `b1780c43e423d9840c8bbf5f9e8a36580d76b83a` | `96178c33a5fd2422b3d1fe7e0f83f89bb0d44885` |
| `store_fname` | `21/21c14c1c5f4d89a998aa5b477f781aadfd8856ad` | `b1/b1780c43e423d9840c8bbf5f9e8a36580d76b83a` | `96/96178c33a5fd2422b3d1fe7e0f83f89bb0d44885` |
| Path in zip | `filestore/21/21c14c1c5f4d89a998aa5b477f781aadfd8856ad` | `filestore/b1/b1780c43e423d9840c8bbf5f9e8a36580d76b83a` | `filestore/96/96178c33a5fd2422b3d1fe7e0f83f89bb0d44885` |
| `base` `latest_version` | `17.0.1.3` | `18.0.1.3` | `19.0.1.3` |
| Installed modules | 11 | 12 | 14 |
| manifest `major_version` | `17.0` | `18.0` | `19.0` |
| manifest `version` | `17.0-20260908` | `18.0-20261015` | `19.0-20261015` |
| manifest `db_name` | `fixture` | `fixture` | `fixture` |
| manifest `pg_version` | `16.0` | `16.0` | `16.0` |
| Rows in `ir_attachment` | 14 | 14 | 14 |
| Rows in `res_users` (active) | 5 (1) | 5 (1) | 4 (1) |
| `database.uuid` | `39f2f2d4-cb43-11f1-9b2c-62b5b900186a` | `27eb0a80-cb43-11f1-ad4f-39ab03fe46e7` | `47e442c6-cb43-11f1-94a5-83d59b60ec2e` |
| Zip entries | 24 | 24 | 24 |
| Zip top-level names | `dump.sql`, `filestore`, `manifest.json` | same | same |
| Files under `filestore/` (excluding `checklist/`) | 11 | 11 | 11 |
| `wrapped.zip` top-level names | `fixture-backup` | same | same |

## File sizes (bytes)

| File | 17.0 | 18.0 | 19.0 |
|---|---|---|---|
| `fixture.zip` | 486987 | 514098 | 530258 |
| `fixture.dump` | 1051313 | 1124009 | 1202825 |
| `wrapped.zip` | 487707 | 514818 | 530978 |
