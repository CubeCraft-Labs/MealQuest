# MealQuest deployment runbook

How MealQuest is deployed on office-srvr (10.60.1.14) and how its data is
backed up and restored. Sections marked *task 876134bc* are completed by
that task (Feature 07 deployment runbook); this file currently records the
reverse-proxy route, the backups and the restore procedure.

Paths used below:

| Name | Path |
| --- | --- |
| Checkout on the host | `/home/overseer/mealquest` |
| Data volume (`mealquest-data`) on the host | `/var/lib/docker/volumes/mealquest-data/_data` |
| Same volume inside the containers | `/data` |
| Database | `mealquest.db` (+ `mealquest.db-wal`, `mealquest.db-shm` while open) |
| Media | `media/` in the data volume |
| Backup target (NAS, over SSH) | `joshua@10.60.1.50:/volume1/homes/Joshua/mealquest-backups` |
| Same target over SFTP (Synology chroot) | `/homes/Joshua/mealquest-backups` |

## Stack and first deploy

*Completed by task 876134bc.* The compose stack is `deploy/compose.yaml`;
start it with `docker compose -f deploy/compose.yaml up -d --build`.

## Reverse proxy (Traefik)

MealQuest is published at <https://mealquest.cubecraftlabs.com> by the
shared Traefik instance on 10.60.1.33 (Feature 07, task 0c1dc172). The
route is a dynamic-configuration file that lives on the Traefik host only,
at `/home/overseer/Code/traefik/config/dynamic/mealquest-ccl.yml`. Its exact
contents:

```yaml
# MealQuest - https://mealquest.cubecraftlabs.com
#   Feature 07 task 0c1dc172. Single Go binary (API + embedded PWA) on
#   office-srvr 10.60.1.14:8090. HTTP -> HTTPS is handled globally by
#   http-conf.yml, so only a websecure router is defined here.
#   This file lives on the Traefik host only; the repo copy is recorded in
#   deploy/RUNBOOK.md by task 876134bc.
http:
  routers:
    mealquest:
      entryPoints:
        - websecure
      rule: "Host(`mealquest.cubecraftlabs.com`)"
      priority: 100
      middlewares:
        - mealquest-headers
      service: mealquest
      tls:
        certResolver: myresolver
        domains:
          - main: mealquest.cubecraftlabs.com

  services:
    mealquest:
      loadBalancer:
        servers:
          - url: "http://10.60.1.14:8090"

  middlewares:
    mealquest-headers:
      headers:
        stsSeconds: 31536000
        stsIncludeSubdomains: true
        contentTypeNosniff: true
        frameDeny: true
```

## Backups

### Database — Litestream

The `litestream` compose service streams every committed change to
`mealquest.db` to the NAS over SFTP (`deploy/litestream.yml`). It takes a
full snapshot daily (compaction level 9) and keeps a week of snapshots
plus the incremental LTX files between them.

One-time setup (already done on 10.60.1.14):

1. Generate the dedicated keypair, git-ignored, owned by the container
   user:

   ```sh
   cd /home/overseer/mealquest
   ssh-keygen -t ed25519 -N '' -C mealquest-litestream -f deploy/secrets/litestream_key
   # append deploy/secrets/litestream_key.pub to joshua@10.60.1.50:~/.ssh/authorized_keys
   rm deploy/secrets/litestream_key.pub
   sudo chown 10001:10001 deploy/secrets/litestream_key
   sudo chmod 0600 deploy/secrets/litestream_key
   ```

2. The NAS host key is pinned in `deploy/litestream.yml` (`host-key`).

Check replication — list every LTX file at every level. Level 9 holds the
full snapshots; the default listing (`-level 0`) shows only L0 files:

```sh
docker compose -f deploy/compose.yaml exec -T litestream \
  litestream ltx -config /etc/litestream.yml -level all /data/mealquest.db
```

### Media — nightly rsync

`mealquest-media-backup.timer` runs `mealquest-media-backup.service` at
03:30 every night. The service runs `/usr/local/sbin/mealquest-media-backup`
(`deploy/systemd/mealquest-media-backup.sh`) as the unprivileged host user
`mealquest` (uid/gid 10001, the app's own user) with no capabilities.
systemd bind-mounts the data volume read-only at `/run/mealquest-data` and
passes the Litestream key in as the credential `nas_key`; uid 10001 cannot
otherwise reach `/var/lib/docker` or `deploy/secrets/`. The script refuses a
`media` path that is a symlink and fails (exit 1) if `media` is missing.

One-time setup (already done on 10.60.1.14):

```sh
cd /home/overseer/mealquest
# Host user for the backup; ssh needs a passwd entry for its uid.
sudo groupadd --system --gid 10001 mealquest
sudo useradd --system --uid 10001 --gid 10001 --no-create-home \
  --home-dir /nonexistent --shell /usr/sbin/nologin mealquest
# Pinned NAS host keys, root-owned and world-readable.
sudo install -d -o root -g root -m 0755 /etc/mealquest
sudo install -o root -g root -m 0644 deploy/known_hosts /etc/mealquest/known_hosts
# The media directory; the backup fails if it is ever missing.
sudo install -d -o 10001 -g 10001 -m 0755 /var/lib/docker/volumes/mealquest-data/_data/media
# Script and units.
sudo install -o root -g root -m 0755 deploy/systemd/mealquest-media-backup.sh /usr/local/sbin/mealquest-media-backup
sudo install -o root -g root -m 0644 deploy/systemd/mealquest-media-backup.service \
  deploy/systemd/mealquest-media-backup.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now mealquest-media-backup.timer
```

Run it by hand and read the result:

```sh
sudo systemctl start mealquest-media-backup.service
journalctl -u mealquest-media-backup --no-pager -n 30
```

Restore media by reversing the copy into the (empty or stale) media
directory as uid 10001:

```sh
sudo rsync -a --chown=10001:10001 \
  -e "ssh -i deploy/secrets/litestream_key -o IdentitiesOnly=yes -o UserKnownHostsFile=/etc/mealquest/known_hosts -o StrictHostKeyChecking=yes" \
  joshua@10.60.1.50:/volume1/homes/Joshua/mealquest-backups/media/ \
  /var/lib/docker/volumes/mealquest-data/_data/media/
```

## Restore the database

SQLite in WAL mode keeps recent commits in `mealquest.db-wal`, indexed by
`mealquest.db-shm`. **A restored database must never sit beside an old WAL
or shared-memory file**: on open, SQLite would replay the old WAL's pages
onto the restored database and corrupt it or mix in state from the
database it replaced. So all three files are moved aside together, with
Litestream's local state directory `.mealquest.db-litestream`, before the
restore — and the restore runs into an empty volume.

Run from `/home/overseer/mealquest`:

```sh
D=/var/lib/docker/volumes/mealquest-data/_data
A=/var/tmp/mealquest-restore-aside-$(date +%Y%m%d%H%M)

# 1. Stop the whole stack (app and Litestream). Nothing may hold the
#    database open; a clean close checkpoints the WAL into mealquest.db.
docker compose -f deploy/compose.yaml down

# 2. Move the database, its -wal and -shm files and Litestream's state
#    aside — all of them, every time. Keep the directory until the
#    restored stack is verified.
sudo mkdir "$A"
for f in mealquest.db mealquest.db-wal mealquest.db-shm .mealquest.db-litestream; do
  if sudo test -e "$D/$f"; then sudo mv "$D/$f" "$A/"; fi
done
sudo ls -la "$D"   # must list only media/

# 3. Restore from the NAS. --no-deps keeps the app stopped so it cannot
#    create a fresh, empty database first.
docker compose -f deploy/compose.yaml run --rm --no-deps litestream \
  restore -config /etc/litestream.yml -o /data/mealquest.db /data/mealquest.db
sudo ls -la "$D"   # mealquest.db only: no -wal, no -shm

# 4. Check the restored file before starting the app (see "Count check").

# 5. Start the stack and check health.
docker compose -f deploy/compose.yaml up -d
docker compose -f deploy/compose.yaml ps
curl -s -o /dev/null -w "health %{http_code}\n" http://10.60.1.14:8090/health
```

To restore to a point in time, add `-timestamp 2026-10-05T23:00:00Z` (or
`-txid`) to the `restore` command.

### Count check

`/tmp/mqcount.py` prints row counts for key tables, the number of
application tables, a SHA-256 over every application row (first 16 hex
digits; Litestream's own `_litestream*` tables are excluded) and
`PRAGMA integrity_check`. Run it on a **copy** of the database, never on the
live file; copy `-wal`/`-shm` along with it when the app is running.

```python
import sqlite3, sys, hashlib
c = sqlite3.connect(sys.argv[1])
tabs = [r[0] for r in c.execute("select name from sqlite_master where type=\"table\" and name not like \"sqlite_%\" and name not like \"_litestream%\" order by name")]
for t in ("dinners", "week_plans", "schema_migrations"):
    print(t, c.execute(f"select count(*) from {t}").fetchone()[0])
h = hashlib.sha256()
for t in tabs:
    for row in c.execute(f"select * from \"{t}\" order by rowid"):
        h.update(repr(row).encode())
print("tables", len(tabs), "content-sha256", h.hexdigest()[:16])
print("integrity", c.execute("pragma integrity_check").fetchone()[0])
```

```sh
rm -rf /tmp/mqcheck && mkdir /tmp/mqcheck
sudo sh -c "cp -a $D/mealquest.db* /tmp/mqcheck/" && sudo chown -R "$USER" /tmp/mqcheck
python3 /tmp/mqcount.py /tmp/mqcheck/mealquest.db
```

Before and after must match exactly.

## Restore drill record — 2026-10-05

Drill on 10.60.1.14 against the live (pre-launch, empty-household)
database, Litestream 0.5.17. Commands as in "Restore the database";
output trimmed only of compose progress lines.

Replica before the drill (`ltx -level all`; level 9 is the full snapshot):

```
level  min_txid          max_txid          size   created
0      0000000000000003  0000000000000003  14110  2026-10-05T23:13:30Z
1      0000000000000001  0000000000000002  14110  2026-10-05T23:12:38Z
1      0000000000000003  0000000000000003  14110  2026-10-05T23:13:30Z
2      0000000000000001  0000000000000002  14110  2026-10-05T23:12:38Z
2      0000000000000003  0000000000000003  14110  2026-10-05T23:13:30Z
3      0000000000000001  0000000000000002  14110  2026-10-05T23:12:38Z
9      0000000000000001  0000000000000002  14110  2026-10-05T23:12:38Z
```

1. `docker compose -f deploy/compose.yaml down` — both containers stopped
   and removed. The volume then held `mealquest.db`,
   `.mealquest.db-litestream/` and `media/`; the clean close had
   checkpointed and removed `-wal` and `-shm`.
2. Before (copy of the stopped database):

   ```
   dinners 0
   week_plans 0
   schema_migrations 7
   tables 26 content-sha256 836e230e16c58dc7
   integrity ok
   ```

3. Moved aside to `/var/tmp/mealquest-restore-aside-20261005`:
   `mealquest.db` and `.mealquest.db-litestream` (no `-wal`/`-shm` existed
   after the clean stop; the loop moves them whenever they do). Volume
   afterwards: `media/` only.
4. `docker compose -f deploy/compose.yaml run --rm --no-deps litestream restore -config /etc/litestream.yml -o /data/mealquest.db /data/mealquest.db`
   — volume afterwards: `mealquest.db` (258048 bytes) and `media/`; no
   `-wal`, no `-shm`.
5. After (copy of the restored file, before the app started):

   ```
   dinners 0
   week_plans 0
   schema_migrations 7
   tables 26 content-sha256 836e230e16c58dc7
   integrity ok
   ```

6. `docker compose -f deploy/compose.yaml up -d` — `mealquest-mealquest-1`
   healthy, `mealquest-litestream-1` up; `GET /health` → `200`. Counts on a
   copy of the running database (`mealquest.db`, `-wal`, `-shm`) were
   identical to the above.
7. Litestream resumed against the restored database:

   ```
   level=INFO msg="detected database behind replica" db=mealquest.db db_txid=0000000000000000 replica_txid=0000000000000003
   level=INFO msg="fetched latest L0 file from replica" db=mealquest.db min_txid=0000000000000003 max_txid=0000000000000003
   level=INFO msg="compaction complete" db=mealquest.db level=1 txid.min=0000000000000004 txid.max=0000000000000004 size=14110
   ```

   Replica after the drill (`ltx -level all`):

   ```
   level  min_txid          max_txid          size   created
   0      0000000000000004  0000000000000004  14110  2026-10-05T23:27:21Z
   1      0000000000000001  0000000000000002  14110  2026-10-05T23:12:38Z
   1      0000000000000003  0000000000000003  14110  2026-10-05T23:13:30Z
   1      0000000000000004  0000000000000004  14110  2026-10-05T23:27:21Z
   2      0000000000000001  0000000000000002  14110  2026-10-05T23:12:38Z
   2      0000000000000003  0000000000000003  14110  2026-10-05T23:13:30Z
   3      0000000000000001  0000000000000002  14110  2026-10-05T23:12:38Z
   9      0000000000000001  0000000000000002  14110  2026-10-05T23:12:38Z
   ```

The first drill earlier the same day (before `-level all` was recorded)
gave the same before/after result: checksum `836e230e16c58dc7`, 26 tables,
integrity ok.

### Media backup checks — 2026-10-05

`sudo systemctl start mealquest-media-backup.service` on 10.60.1.14:

| Case | Result |
| --- | --- |
| Normal run | `Result=success`; journal: `synced /run/mealquest-data/media to joshua@10.60.1.50:/volume1/homes/Joshua/mealquest-backups/media/ as uid 10001` |
| A test file added | `Number of regular files transferred: 1`; removing it then gave `Number of deleted files: 1 (reg: 1)` |
| `media` replaced by a symlink to `/etc` | `Result=exit-code`, status 1; `/run/mealquest-data/media is a symlink to /etc; refusing to back it up` |
| `media` removed | `Result=exit-code`, status 1; `media directory /run/mealquest-data/media is missing` |
| `sudo -u mealquest` writing into the volume via `/var/lib/docker/...` | `Permission denied` — uid 10001 cannot traverse `/var/lib/docker` (mode 0710 root) |
