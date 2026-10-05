# MealQuest deployment runbook

How MealQuest is deployed on office-srvr (10.60.1.14), how it is reached
through Authentik, Traefik and DNS, how it is updated and rolled back, how
its data is backed up and restored, and how its secrets are rotated. Values
are the ones in use on 2026-10-05. Secrets themselves are never written
here.

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

## Prerequisites

On office-srvr (10.60.1.14), as recorded on 2026-10-05:

| Item | Value |
| --- | --- |
| OS | Ubuntu 26.04.1 LTS |
| Docker Engine / Compose plugin | 29.8.2 / 5.6.0 |
| git, rsync, iptables | 2.53.0, 3.4.1, 1.8.11 (nf_tables) |
| Account | `overseer`, with sudo |
| Network | LAN reachability to Traefik (10.60.1.33), Authentik (`auth.cubecraftlabs.com`) and the NAS (10.60.1.50, SSH) |

Elsewhere:

- Admin access to Authentik at <https://auth.cubecraftlabs.com>.
- SSH access to the Traefik host 10.60.1.33.
- Cloudflare access for the `cubecraftlabs.com` zone, and admin access to
  both Pi-holes (10.60.1.101 `dns.ccl`, 10.60.1.85 `dns2.ccl`).
- A NAS account `joshua` on 10.60.1.50 with the backup directory
  `/volume1/homes/Joshua/mealquest-backups` (see "Backups").
- A Gitea token able to read `Maniacs_Mansion/MealQuest`, for the clone.

## Stack and first deploy

The compose stack is `deploy/compose.yaml`: the `mealquest` service (built
from the repository `Dockerfile`, runs as uid/gid 10001, listens on 8080 in
the container) and the `litestream` service. Data lives in the named volume
`mealquest-data`.

### 1. Choose and check the port

The container's 8080 is published on `10.60.1.14:8090`. Ports 8080 and 8081
on this host belong to NestQuest, so 8090 was chosen. Before the first
deploy it was checked free with:

```sh
ss -ltnp | grep 8090   # printed nothing
```

After the deploy the same host shows NestQuest on 8080/8081 and MealQuest
on 8090:

```
LISTEN 0 4096 10.60.1.14:8080 0.0.0.0:*
LISTEN 0 4096 10.60.1.14:8081 0.0.0.0:*
LISTEN 0 4096 10.60.1.14:8090 0.0.0.0:*
```

The published host port is fixed in `deploy/compose.yaml`, in
`deploy/systemd/mealquest-firewall.sh` and in the Traefik route; change all
three together.

### 2. Clone

The checkout lives at `/home/overseer/mealquest`, next to NestQuest's
`~/nestquest` (not `/opt/mealquest`). Clone over HTTPS with a Gitea token,
then reset the remote to the clean URL so the token is not stored in
`.git/config`:

```sh
cd /home/overseer
git clone https://<user>:<token>@code.cubecraftlabs.com/Maniacs_Mansion/MealQuest.git mealquest
cd mealquest
git remote set-url origin https://code.cubecraftlabs.com/Maniacs_Mansion/MealQuest.git
git remote -v   # must show no token
git checkout main
```

### 3. Environment file

`deploy/.env` (git-ignored) is copied from `deploy/.env.example` and is
mode 0600, owned by `overseer`:

```sh
cp deploy/.env.example deploy/.env
chmod 600 deploy/.env
```

Values in use (the client id is not a secret but is read from the
Authentik provider, not repeated here):

| Variable | Value |
| --- | --- |
| `MEALQUEST_BIND_ADDRESS` | `10.60.1.14` |
| `MEALQUEST_PUBLIC_URL` | `https://mealquest.cubecraftlabs.com` |
| `MEALQUEST_SHUTDOWN_TIMEOUT` | `10s` |
| `MEALQUEST_TRUSTED_PROXY` | `10.60.1.33` |
| `MEALQUEST_DEFAULT_TZ` | `America/New_York` |
| `MEALQUEST_OIDC_ISSUER` | `https://auth.cubecraftlabs.com/application/o/mealquest/` |
| `MEALQUEST_OIDC_CLIENT_ID` | the Client ID of the Authentik provider `mealquest` |
| `MEALQUEST_OIDC_ALLOWED_GROUPS` | `mealquest-admin` |
| `MEALQUEST_SESSION_IDLE` / `MEALQUEST_SESSION_MAX` | `720h` / `4320h` |

`MEALQUEST_HTTP_ADDR`, `MEALQUEST_DATA_DIR` and the `*_FILE` variables are
set by `deploy/compose.yaml` and must not appear in `deploy/.env`.

### 4. Secrets

`deploy/secrets/` (git-ignored) is mode 0700, owned by `overseer`. Each
file holds one value, is owned by `10001:10001` (host user `mealquest`) and
is mode 0600. Compose bind-mounts file secrets with their host owner and
mode, so any other owner or mode stops the container reading them and the
service refuses to start.

| File | Content | Mounted at |
| --- | --- | --- |
| `secret_key` | base64 of 32 random bytes; encrypts stored secrets (AES-256-GCM) | `/run/secrets/secret_key` |
| `oidc_client_secret` | Client Secret of the Authentik provider `mealquest` | `/run/secrets/oidc_client_secret` |
| `litestream_key` | dedicated SSH private key for the NAS (see "Backups") | `/etc/litestream/sftp_key` |

```sh
mkdir -p deploy/secrets && chmod 700 deploy/secrets
head -c 32 /dev/urandom | base64 > deploy/secrets/secret_key
printf '%s\n' '<Authentik client secret>' > deploy/secrets/oidc_client_secret
# litestream_key: see "Backups" > "Database — Litestream"
sudo chown 10001:10001 deploy/secrets/*
sudo chmod 600 deploy/secrets/*
sudo ls -la deploy/secrets   # three files, mealquest mealquest, -rw-------
```

The `mealquest` host user (uid/gid 10001) is created in "Media — nightly
rsync"; until it exists `ls` shows the numeric ids.

### 5. Firewall, then the stack

Install the firewall unit first (see "Host firewall"), so port 8090 is
never open to the LAN. Create the media directory (see "Media — nightly
rsync"), then build and start:

```sh
sudo systemctl start mealquest-firewall
docker compose -f deploy/compose.yaml up -d --build
docker compose -f deploy/compose.yaml ps   # mealquest healthy, litestream up
curl -fsS http://10.60.1.14:8090/health
curl -s -o /dev/null -w "%{http_code}\n" https://mealquest.cubecraftlabs.com/health   # 200
```

### Host checkout state on 2026-10-05

The first deploy ran before Feature 07 reached `main`. The checkout is at
`main` `9cddbbb` and the Feature 07 files under `deploy/` were copied in
from the feature branch: `deploy/compose.yaml` and `deploy/.env.example`
are listed in `.git/info/exclude`, the rest show as untracked. Before the
first routine deploy of a `main` that contains `deploy/`, remove those
`.git/info/exclude` lines and the copied files (keep `deploy/.env` and
`deploy/secrets/`), or `git checkout` refuses to overwrite them:

```sh
cd /home/overseer/mealquest
git status --porcelain --untracked-files=all deploy   # the copied files
sed -i '/^deploy\/\(compose.yaml\|.env.example\)$/d' .git/info/exclude
git clean -n deploy      # dry run: must not list .env or secrets/ (both git-ignored)
git clean -f deploy
```

Then follow "Routine deploy"; the checkout brings the tracked copies back.

## Authentik

Sign-in is OpenID Connect against Authentik at
<https://auth.cubecraftlabs.com>.

| Object | Setting |
| --- | --- |
| Provider | OAuth2/OpenID provider `mealquest`, client type **Confidential** |
| Redirect URI | `https://mealquest.cubecraftlabs.com/auth/callback` (strict) |
| Signing | RS256, with the instance's signing key |
| Scopes | MealQuest requests `openid profile email groups offline_access`; the provider's scope mappings include openid, profile, email and offline_access, and the `groups` claim is delivered by a property mapping (the default profile mapping carries `groups`) |
| Application | name MealQuest, slug `mealquest`, provider `mealquest` |
| Group | `mealquest-admin`, bound to the application; household members are added to it |
| Client ID | in `deploy/.env` as `MEALQUEST_OIDC_CLIENT_ID` |
| Client Secret | in `deploy/secrets/oidc_client_secret` only |

MealQuest refuses a sign-in unless the user's `groups` claim (from the ID
token, or from userinfo when the token has none) contains a group in
`MEALQUEST_OIDC_ALLOWED_GROUPS`.

Check discovery from the Docker host — the service discovers the issuer
at boot and will not start if it cannot:

```sh
curl -s -o /dev/null -w "%{http_code}\n" \
  https://auth.cubecraftlabs.com/application/o/mealquest/.well-known/openid-configuration   # 200
```

On 2026-10-05 discovery returned 200 with issuer
`https://auth.cubecraftlabs.com/application/o/mealquest/` and
`id_token_signing_alg_values_supported: ["RS256"]`.

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

Traefik watches the dynamic directory, so saving the file loads the route;
the certificate comes from `certResolver: myresolver` (Let's Encrypt,
DNS-01 through Cloudflare). Check from the LAN:

```sh
curl -s -o /dev/null -w "%{http_code}\n" https://mealquest.cubecraftlabs.com/health   # 200
```

## DNS

| Where | Record |
| --- | --- |
| Cloudflare, zone `cubecraftlabs.com` | `A mealquest` → the public address, **proxied** (public resolvers answer with Cloudflare addresses) |
| Pi-hole primary 10.60.1.101 (`dns.ccl`), Local DNS records | `mealquest.cubecraftlabs.com` → `10.60.1.33` |
| Pi-hole secondary 10.60.1.85 (`dns2.ccl`), Local DNS records | `mealquest.cubecraftlabs.com` → `10.60.1.33` |

The local record must be on **both** Pi-holes, or clients using the
secondary go out through Cloudflare. Check:

```sh
for s in 10.60.1.101 10.60.1.85; do dig +short @$s mealquest.cubecraftlabs.com A; done
# 10.60.1.33 from each
```

**AAAA caveat.** The Pi-hole local record is A only, so both Pi-holes still
forward AAAA queries and return Cloudflare's IPv6 addresses
(`2606:4700:3032::6815:34ed`, `2606:4700:3035::ac43:cd75` on 2026-10-05). An
IPv6-capable LAN client may prefer those and reach MealQuest through
Cloudflare instead of directly through Traefik. It still works (Cloudflare
proxies to the public route), but it is not the LAN path. If that matters,
add a local AAAA override or block AAAA for this name on both Pi-holes.

## Host firewall

Docker publishes `10.60.1.14:8090` past the host's INPUT rules, so the
port is restricted in the `DOCKER-USER` chain instead: only Traefik
(10.60.1.33) and the host itself (10.60.1.14) may connect; every other
source is dropped.

Docker DNATs the published port to the container address and port 8080
**before** `DOCKER-USER`, so a plain `-p tcp --dport 8090` rule never
matches. The rules match the original destination `10.60.1.14:8090` with
conntrack (`--ctorigdst 10.60.1.14 --ctorigdstport 8090 --ctdir ORIGINAL`)
and are tagged with the comment `mealquest-firewall`. The script deletes
its own tagged rules and re-inserts them at positions 1–3, so re-runs are
idempotent. NestQuest's 8080/8081 rules below them are left untouched.

| Repository file | Installed as |
| --- | --- |
| `deploy/systemd/mealquest-firewall.sh` | `/usr/local/sbin/mealquest-firewall` (root, 0755) |
| `deploy/systemd/mealquest-firewall.service` | `/etc/systemd/system/mealquest-firewall.service` (root, 0644) |

The unit is `Type=oneshot`, `RemainAfterExit=yes`, `Requires=` and
`PartOf=docker.service`, and `WantedBy=multi-user.target docker.service`:
it re-applies the rules whenever Docker starts or restarts (Docker
recreates `DOCKER-USER` empty), and a failure fails this unit only.

Install (done on 10.60.1.14 on 2026-10-05):

```sh
cd /home/overseer/mealquest
sudo install -o root -g root -m 0755 deploy/systemd/mealquest-firewall.sh /usr/local/sbin/mealquest-firewall
sudo install -o root -g root -m 0644 deploy/systemd/mealquest-firewall.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now mealquest-firewall
```

Check:

```sh
systemctl is-enabled mealquest-firewall; systemctl is-active mealquest-firewall   # enabled, active
sudo iptables -S DOCKER-USER
```

Expected head of the chain (recorded 2026-10-05; NestQuest's rules follow):

```
-A DOCKER-USER -s 10.60.1.33/32 -m conntrack --ctorigdst 10.60.1.14 --ctorigdstport 8090 --ctdir ORIGINAL -m comment --comment mealquest-firewall -j RETURN
-A DOCKER-USER -s 10.60.1.14/32 -m conntrack --ctorigdst 10.60.1.14 --ctorigdstport 8090 --ctdir ORIGINAL -m comment --comment mealquest-firewall -j RETURN
-A DOCKER-USER -m conntrack --ctorigdst 10.60.1.14 --ctorigdstport 8090 --ctdir ORIGINAL -m comment --comment mealquest-firewall -j DROP
```

From any other LAN machine, `curl -m 5 http://10.60.1.14:8090/health` must
time out; from 10.60.1.33 and 10.60.1.14 it returns 200.

## Routine deploy and rollback

Production runs `main` (a release); only `overseer` merges `dev` → `main`.
Every deploy builds the image on the host from the checked-out commit.

### Deploy

```sh
cd /home/overseer/mealquest
git fetch origin
git rev-parse --short HEAD          # the SHA being replaced: note it for rollback
git checkout --detach origin/main   # or a release tag
docker compose -f deploy/compose.yaml up -d --build
docker compose -f deploy/compose.yaml ps   # mealquest healthy, litestream up
curl -fsS http://10.60.1.14:8090/health
curl -s -o /dev/null -w "%{http_code}\n" https://mealquest.cubecraftlabs.com/health   # 200
git rev-parse HEAD                  # the deployed SHA
```

Record the deployed SHA, the previous SHA and the date in the Maestro task
for the release. Migrations run at start-up; the service refuses to start
if the database has an applied migration it does not know.

If `deploy/.env.example` gained a variable, add it to `deploy/.env` before
`up`; the service fails fast at boot naming any missing required variable.

### Rollback to a previous SHA

```sh
cd /home/overseer/mealquest
git checkout --detach <previous SHA>
docker compose -f deploy/compose.yaml up -d --build
docker compose -f deploy/compose.yaml ps
curl -fsS http://10.60.1.14:8090/health
```

A rollback across a release that added a migration fails at start-up
(the older binary refuses the newer schema). In that case restore the
database to a point before the upgrade — "Restore the database" with
`-timestamp` set just before the deploy — then start the older SHA.
Changes made since that point are lost; say so to the household.

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

## Restore photos (media)

Recipe and dinner photos live in `media/` in the data volume and are
backed up nightly to `joshua@10.60.1.50:/volume1/homes/Joshua/mealquest-backups/media/`
(see "Media — nightly rsync"). The nightly job mirrors with `--delete`:
**stop the timer first**, or a run against an emptied or partial `media/`
would delete the NAS copy you are about to restore from.

Run from `/home/overseer/mealquest`:

```sh
M=/var/lib/docker/volumes/mealquest-data/_data/media

# 1. Stop the nightly mirror and the app (no writes into media/ meanwhile).
sudo systemctl stop mealquest-media-backup.timer
docker compose -f deploy/compose.yaml stop mealquest

# 2. Recreate the directory if it is gone, owned by the app user.
sudo install -d -o 10001 -g 10001 -m 0755 "$M"

# 3. Copy back from the NAS as uid 10001's files.
sudo rsync -a --stats --chown=10001:10001 \
  -e "ssh -i deploy/secrets/litestream_key -o IdentitiesOnly=yes -o UserKnownHostsFile=/etc/mealquest/known_hosts -o StrictHostKeyChecking=yes" \
  joshua@10.60.1.50:/volume1/homes/Joshua/mealquest-backups/media/ \
  "$M/"
sudo find "$M" -type f | wc -l   # compare with the file count on the NAS

# 4. Start the app and the timer again.
docker compose -f deploy/compose.yaml up -d
curl -fsS http://10.60.1.14:8090/health
sudo systemctl start mealquest-media-backup.timer
```

When the database is restored too, restore it first ("Restore the
database"), then the media, so every photo the database refers to is
present. This media restore has not been run against production yet; the
backup direction was checked on 2026-10-05 (see "Media backup checks").

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

## Secret rotation

Rotate a secret when it may have leaked, when someone with access leaves,
or on the owner's schedule. Never paste a secret into a ticket, chat or
this file.

### App key (`deploy/secrets/secret_key`)

The app key encrypts every stored secret (the `_ct` columns: the LLM API
key, the recipe-search API key, the Google Calendar refresh token and the
Google Chat webhook URL).
**Rotating it re-encrypts nothing.** Values encrypted with the old key can
no longer be read and must be entered again in the app. A
`mealquest rotate-key` helper that re-encrypts in place is planned but does
not exist yet.

```sh
cd /home/overseer/mealquest
sudo cp -a deploy/secrets/secret_key /var/tmp/secret_key.old   # keep until verified, then shred
head -c 32 /dev/urandom | base64 | sudo tee deploy/secrets/secret_key >/dev/null
sudo chown 10001:10001 deploy/secrets/secret_key
sudo chmod 600 deploy/secrets/secret_key
docker compose -f deploy/compose.yaml up -d --force-recreate mealquest
curl -fsS http://10.60.1.14:8090/health
```

Then re-enter every stored secret (the LLM key in Settings → AI assistant,
the recipe-search key, reconnect Google Calendar, the Google Chat webhook)
and confirm each works.
Finally `sudo shred -u /var/tmp/secret_key.old`.

### OIDC client secret (`deploy/secrets/oidc_client_secret`)

In Authentik, open the provider `mealquest` and regenerate the Client
Secret. Write it to the file (same owner and mode as above) and recreate
the service:

```sh
printf '%s\n' '<new Authentik client secret>' | sudo tee deploy/secrets/oidc_client_secret >/dev/null
sudo chown 10001:10001 deploy/secrets/oidc_client_secret
sudo chmod 600 deploy/secrets/oidc_client_secret
docker compose -f deploy/compose.yaml up -d --force-recreate mealquest
```

Sign in at <https://mealquest.cubecraftlabs.com> to confirm; sign-ins
fail between regenerating the secret in Authentik and recreating the
service.

### LLM API key

Stored encrypted in the database, not in `deploy/`. In the app, open
**Settings → AI assistant**, choose **Replace**, paste the new key and
**Verify**, and check the verified state is shown. Then revoke the
old key in the provider's console. (The AI assistant settings page is
delivered by its own feature; until it ships there is no stored LLM key
to rotate.)

### VAPID keys (Web Push)

Planned with Web Push (Feature 19): the public key goes in
`deploy/.env` as `MEALQUEST_VAPID_PUBLIC_KEY`, the private key in
`deploy/secrets/vapid_private_key` (owner `10001:10001`, mode 0600), and
the pair is generated with `cmd/vapidgen`. **Changing the VAPID pair
invalidates every push subscription**: each device must turn
notifications on again (`/settings/notifications`) before it receives
pushes. Rotate only when the private key may have leaked, write both
halves, recreate the service with `--force-recreate mealquest`, and tell
the household to re-enable notifications on each device.

### Litestream / NAS key (`deploy/secrets/litestream_key`)

Generate a new keypair as in "Database — Litestream", add the new public
key to `joshua@10.60.1.50:~/.ssh/authorized_keys`, replace the file
(owner `10001:10001`, mode 0600), run
`docker compose -f deploy/compose.yaml up -d --force-recreate litestream`
and `sudo systemctl start mealquest-media-backup.service`, check both
(`litestream ltx` and the journal), then remove the old public key from
the NAS.

## Context records

The confirmed values in this runbook (port 8090, checkout path, Authentik
group `mealquest-admin`, DNS, firewall, backup target) are also recorded in
the CTXD project `mealquest`: `RUNBOOKS.MD` (deployment procedure) and the
open questions in `CONTEXT.MD`, updated for Feature 07 task 876134bc.
