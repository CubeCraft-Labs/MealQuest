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
- Owner access to the Google Cloud project that holds MealQuest's OAuth
  client (see "Google Calendar OAuth client").
- SSH access to the Traefik host 10.60.1.33.
- Cloudflare access for the `cubecraftlabs.com` zone, and admin access to
  both Pi-holes (10.60.1.101 `dns.ccl`, 10.60.1.85 `dns2.ccl`).
- A NAS account `joshua` on 10.60.1.50 with the backup directory
  `/volume1/homes/Joshua/mealquest-backups` (see "Backups").
- A Gitea token able to read `Maniacs_Mansion/MealQuest`, for the clone.

## Stack and first deploy

The compose stack is `deploy/compose.yaml`: the `mealquest` service (built
from the repository `Dockerfile`, runs as uid/gid 10001, listens on 8080 in
the container), the `litestream` service and the `searxng` recipe-search
sidecar (see "Recipe search (SearXNG)"). Data lives in the named volume
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

Then edit `deploy/.env` to the values below. In particular
**`MEALQUEST_BIND_ADDRESS` must be changed**: `.env.example` ships
`127.0.0.1` (a dev machine), and left as is the service listens only on
the host's loopback, which Traefik cannot reach. Set it to the LAN address
and check:

```sh
sed -i 's/^MEALQUEST_BIND_ADDRESS=.*/MEALQUEST_BIND_ADDRESS=10.60.1.14/' deploy/.env
grep '^MEALQUEST_BIND_ADDRESS=' deploy/.env      # MEALQUEST_BIND_ADDRESS=10.60.1.14
docker compose -f deploy/compose.yaml config | grep -A4 'ports:'
#   host_ip: 10.60.1.14, target: 8080, published: "8090"
```

After step 5 the listener is `10.60.1.14:8090` (container port 8080):

```sh
docker compose -f deploy/compose.yaml port mealquest 8080   # 10.60.1.14:8090
sudo ss -ltnp 'sport = :8090'                               # 10.60.1.14:8090 only, no 0.0.0.0 or 127.0.0.1
```

Values in use:

| Variable | Value |
| --- | --- |
| `MEALQUEST_BIND_ADDRESS` | `10.60.1.14` |
| `MEALQUEST_PUBLIC_URL` | `https://mealquest.cubecraftlabs.com` |
| `MEALQUEST_SHUTDOWN_TIMEOUT` | `10s` |
| `MEALQUEST_TRUSTED_PROXY` | `10.60.1.33` |
| `MEALQUEST_DEFAULT_TZ` | `America/New_York` |
| `MEALQUEST_OIDC_ISSUER` | `https://auth.cubecraftlabs.com/application/o/mealquest/` |
| `MEALQUEST_OIDC_CLIENT_ID` | `i6zraqmWsF552b8nmte070LXz0SI5pRdArBxFxeq` (not a secret) |
| `MEALQUEST_OIDC_ALLOWED_GROUPS` | `mealquest-admin` |
| `MEALQUEST_GOOGLE_CLIENT_ID` | Client ID of the Google OAuth web client (not a secret; see "Google Calendar OAuth client") |
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
| `google_client_secret` | Client secret of the Google OAuth web client | `/run/secrets/google_client_secret` |
| `litestream_key` | dedicated SSH private key for the NAS (see "Backups") | `/etc/litestream/sftp_key` |
| `searxng.env` | `SEARXNG_SECRET=<64 hex characters>`; SearXNG's secret key | not mounted; compose passes it as an environment variable |

`searxng.env` is the one exception to the owner rule: compose itself reads
it (as `env_file`), so it is owned by `overseer`, mode 0600.

```sh
mkdir -p deploy/secrets && chmod 700 deploy/secrets
head -c 32 /dev/urandom | base64 > deploy/secrets/secret_key
printf '%s\n' '<Authentik client secret>' > deploy/secrets/oidc_client_secret
printf '%s\n' '<Google OAuth client secret>' > deploy/secrets/google_client_secret
# litestream_key: see "Backups" > "Database — Litestream"
sudo chown 10001:10001 deploy/secrets/secret_key deploy/secrets/oidc_client_secret deploy/secrets/google_client_secret deploy/secrets/litestream_key
sudo chmod 600 deploy/secrets/*
(umask 077; printf 'SEARXNG_SECRET=%s\n' "$(openssl rand -hex 32)" > deploy/secrets/searxng.env)
sudo ls -la deploy/secrets   # four files mealquest mealquest, searxng.env overseer overseer; all -rw-------
```

The `mealquest` host user (uid/gid 10001) is created in "Media — nightly
rsync"; until it exists `ls` shows the numeric ids.

### 5. Firewall, then the stack

Install the firewall unit and the stack unit first (see "Host firewall"
and "Stack unit"), so port 8090 is never open to the LAN. Create the media
directory (see "Media — nightly rsync"), then build and start:

```sh
docker compose -f deploy/compose.yaml build
sudo systemctl enable --now mealquest   # starts mealquest-firewall first
docker compose -f deploy/compose.yaml ps   # mealquest healthy, litestream up, searxng healthy
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

On 2026-10-06 `deploy/compose.yaml` was replaced and
`deploy/compose.dev.yaml` and `deploy/searxng/settings.yml` were copied in
from the Feature 10 branch (task 641f468b) to start the SearXNG sidecar;
they too must go before that checkout (`git clean -n -d deploy` lists
them).

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
| Client ID | `i6zraqmWsF552b8nmte070LXz0SI5pRdArBxFxeq` (not a secret; in `deploy/.env` as `MEALQUEST_OIDC_CLIENT_ID`) |
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

## Google Calendar OAuth client

Push to Calendar connects the household's Google account with a Google
OAuth web client. MealQuest calls Google's OAuth and Calendar REST
endpoints directly. **The service will not start without
`MEALQUEST_GOOGLE_CLIENT_ID` and `deploy/secrets/google_client_secret`**,
so create the client before deploying a release that contains Feature 16.

In the Google Cloud console, in the project that holds MealQuest:

| Object | Setting |
| --- | --- |
| API | Google Calendar API enabled |
| OAuth consent screen | User type External; app name MealQuest; publishing status **In production** (a client left in Testing gets refresh tokens that expire after 7 days, so Push to Calendar would stop working a week after connecting) |
| Scopes | `https://www.googleapis.com/auth/calendar.events` and `https://www.googleapis.com/auth/calendar.calendarlist.readonly` |
| Credentials | OAuth client ID, application type **Web application**, name `mealquest` |
| Authorized redirect URI | `https://mealquest.cubecraftlabs.com/api/v1/integrations/google/callback` (exactly; it is `MEALQUEST_PUBLIC_URL`'s origin plus that path) |
| Client ID | in `deploy/.env` as `MEALQUEST_GOOGLE_CLIENT_ID` (not a secret) |
| Client secret | in `deploy/secrets/google_client_secret` only |

The app is unverified, so Google shows an "unverified app" warning on the
consent screen; choose to continue. That is expected for a single-household
deployment.

To connect, a signed-in household member opens
`https://mealquest.cubecraftlabs.com/api/v1/integrations/google/connect`
(the Settings screen links there once it ships). MealQuest asks for offline
access with consent always prompted, and lands back on
`/settings/calendar` once the refresh token is stored. Disconnecting
(`DELETE /api/v1/integrations/google`) revokes the token at Google and
forgets it.

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
| Cloudflare, zone `cubecraftlabs.com` | `CNAME mealquest.cubecraftlabs.com` → `cubecraftlabs.com`, **proxied = true** (orange cloud). Because it is proxied, public resolvers answer with Cloudflare's anycast addresses, A and AAAA (AAAA on 2026-10-05: `2606:4700:3032::6815:34ed`, `2606:4700:3035::ac43:cd75`), never the origin |
| Pi-hole primary 10.60.1.101 (`dns.ccl`), Local DNS records | `A mealquest.cubecraftlabs.com` → `10.60.1.33` (Traefik) |
| Pi-hole secondary 10.60.1.85 (`dns2.ccl`), Local DNS records | `A mealquest.cubecraftlabs.com` → `10.60.1.33` (Traefik) |

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
and are tagged with the comment `mealquest-firewall`. The port is never
open while the rules change: if the chain does not already start with
exactly the three rules, the script first inserts the DROP at position 1,
then deletes its other tagged rules and inserts the two RETURN rules above
the DROP. A re-run on a correct chain changes nothing. NestQuest's
8080/8081 rules below them are left untouched.

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

## Stack unit

`deploy/systemd/mealquest.service` owns the compose stack and is the only
way it starts. Both compose services use `restart: "no"`, so Docker never
starts them on its own; the unit has `Requires=` and
`After=mealquest-firewall.service`, so the stack starts only after the
firewall rules are applied, and not at all if that unit fails. It is
`PartOf=docker.service`, so restarting Docker (which recreates
`DOCKER-USER` empty) stops the stack, re-applies the firewall, then starts
the stack again. `ExecStart` is `docker compose … up -d` and `ExecStop` is
`docker compose … down`; neither builds, so build before starting.

Containers no longer restart after a crash; `systemctl restart mealquest`
brings them back.

| Repository file | Installed as |
| --- | --- |
| `deploy/systemd/mealquest.service` | `/etc/systemd/system/mealquest.service` (root, 0644) |

Install (done on 10.60.1.14 on 2026-10-05):

```sh
cd /home/overseer/mealquest
sudo install -o root -g root -m 0644 deploy/systemd/mealquest.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now mealquest
```

Check:

```sh
systemctl is-enabled mealquest; systemctl is-active mealquest   # enabled, active
systemctl show mealquest -p Requires -p After                    # both list mealquest-firewall.service
```

Start, stop and restart the whole stack with `sudo systemctl start|stop|restart
mealquest`, never with `docker compose up` or `down`. Per-service compose
commands (`stop mealquest`, `up -d --force-recreate litestream`) are fine
while the unit is active, because the firewall is already in place.

## Recipe search (SearXNG)

The `searxng` service in `deploy/compose.yaml` is a private SearXNG
instance for recipe search. On the compose network it is reachable at
`http://searxng:8080`; that is the address to configure in the app.

Starting the sidecar does not connect the app to it. The recipe-search
provider and base URL live in the `recipe_search_settings` row and are set
through `GET`/`PATCH /api/v1/settings/recipe-search` (the API behind the
recipe search card on Settings → AI assistant). Until that row exists,
recipe search answers 409 `not_configured`.

`deploy/compose.yaml` sets `MEALQUEST_SEARXNG_URL=http://searxng:8080`.
The service only suggests it: `GET` returns it as `default_base_url`, and
the recipe search card pre-fills the SearXNG URL with it while no URL is
saved. Nothing is stored until the card is saved, and a saved `base_url`
always wins. A set value must be an absolute `http://` or `https://` URL
without credentials, a query or a fragment, or the service refuses to
start; unset, `default_base_url` is `null`.

To set it up, open Settings → AI assistant, check that the recipe search
card shows SearXNG with `http://searxng:8080`, select **Save recipe search**
and then **Test**. Or, from a signed-in session (every non-`GET` request
needs the `X-MealQuest-CSRF` header set to `csrfToken` from
`GET /api/v1/me`):

1. `PATCH /api/v1/settings/recipe-search` with
   `{"provider": "searxng", "base_url": "http://searxng:8080"}`.
2. `GET /api/v1/settings/recipe-search` shows `"provider": "searxng"` and
   `"base_url": "http://searxng:8080"`.
3. `POST /api/v1/settings/recipe-search/test` searches
   "chicken tikka masala" and must answer 200 with
   `{"query": "chicken tikka masala", "count": <n>}`. A 502
   `search_failed` carries SearXNG's error.

| Item | Value |
| --- | --- |
| Image | `searxng/searxng:2026.10.4-d48c4b555`, pinned by digest `sha256:76b0bf285aca014c7191fc4d9234c4bfb358624ac33d8883833d496c059ec072` |
| Settings | `deploy/searxng/settings.yml`, mounted read-only at `/etc/searxng/settings.yml` |
| Secret | `deploy/secrets/searxng.env` (`SEARXNG_SECRET`), see "Secrets" |
| Network | `mealquest` only; **no published port** |
| Health | `wget http://127.0.0.1:8080/healthz` inside the container |

The settings enable the `json` output format, turn off the public limiter
and bot detection (the only client is the `mealquest` service), and keep
only the general web search engines DuckDuckGo, Brave, Google, Bing,
Mojeek and Startpage. Engines that block the host (CAPTCHA, "too many
requests") are suspended by SearXNG for a while and listed under
`unresponsive_engines` in the JSON; the others still answer.

Check from inside the app container (the only place it is reachable):

```sh
docker compose -f deploy/compose.yaml exec mealquest \
  wget -qO- 'http://searxng:8080/search?q=chicken+tikka+masala+recipe&format=json'
docker compose -f deploy/compose.yaml port searxng 8080   # prints ":0": nothing published
```

On 2026-10-06 that query returned 17 results. From this host four of the
six engines were blocked (Brave, DuckDuckGo, Google and Startpage were
rate-limited or served a CAPTCHA), so the results came from Bing and
Mojeek. SearXNG retries suspended engines after their suspension ends, so
the set that answers can change. From 10.60.1.85 nothing on 10.60.1.14 answered as SearXNG:
8888 refused the connection and 8080 is NestQuest's API.

For development, `deploy/compose.dev.yaml` publishes SearXNG on the
dev machine's loopback only (`127.0.0.1:8888`). It is never used on
office-srvr.

To move to a newer image, pull the new tag, take its digest from
`docker image inspect --format '{{json .RepoDigests}}' searxng/searxng:<tag>`,
update both in `deploy/compose.yaml`, then deploy as usual.

## Routine deploy and rollback

Production runs `main` (a release); only `overseer` merges `dev` → `main`.
Every deploy builds the image on the host from the checked-out commit.

### Deploy

1. Check out and build the release. Nothing restarts yet; production
   keeps running the previous SHA.

```sh
cd /home/overseer/mealquest &&
git fetch origin &&
git rev-parse --short HEAD &&         # the SHA being replaced: note it for rollback
git checkout --detach origin/main &&  # or a release tag
docker compose -f deploy/compose.yaml build
```

   Each command runs only if the one before it succeeded. If `echo $?`
   then prints anything but `0`, stop: nothing has restarted.

2. **Verify a restorable pre-deploy point — before the restart.** The
   restart applies any new migrations, and an older SHA refuses the
   upgraded schema, so the only rollback is restoring the database to a
   point before the restart (see "Rollback to a previous SHA"). Check that
   Litestream is replicating and that its replica restores to the current
   data, with the commands from "Database — Litestream", "Restore the
   database" and "Count check". First write `/tmp/mqcount.py` and
   `/tmp/mqsnap.py` with the `cat` command in "Count check", then:

```sh
cd /home/overseer/mealquest && (
set -eu   # any failed step ends the verification with a nonzero status
D=/var/lib/docker/volumes/mealquest-data/_data
fail() { echo "$*" >&2; exit 1; }
point=
# On the way out, run both clean-ups whatever happens, keep any failure,
# and print the restore point only if every check and clean-up succeeded.
trap 'st=$?
rm -rf /tmp/mqcheck || st=1
sudo sh -c "rm -f $D/predeploy-check.db*" || st=1
if [ "$st" -eq 0 ] && [ -n "$point" ]; then echo "pre-deploy restore point: $point"
else [ "$st" -ne 0 ] || st=1; echo "verification FAILED (status $st): do not restart" >&2; fi
exit "$st"' EXIT
rm -rf /tmp/mqcheck
mkdir /tmp/mqcheck

# a. Replication is running: litestream up, no errors in its log, and the
#    newest LTX file (listed) is from the latest writes. Each command's own
#    status is checked: grep exits 0 on a match, 1 on none, 2+ on an error.
id=$(docker compose -f deploy/compose.yaml ps --status running --quiet litestream) || fail "docker compose ps failed"
[ -n "$id" ] || fail "litestream is not running"
docker compose -f deploy/compose.yaml logs --since 24h litestream > /tmp/mqcheck/litestream.log || fail "docker compose logs failed"
gs=0
grep -i error /tmp/mqcheck/litestream.log > /tmp/mqcheck/litestream.errors || gs=$?
case "$gs" in
  0) cat /tmp/mqcheck/litestream.errors; fail "litestream logged errors in the last 24h";;
  1) ;;   # no match: no errors logged
  *) fail "the litestream log scan failed (status $gs)";;
esac
docker compose -f deploy/compose.yaml exec -T litestream \
  litestream ltx -config /etc/litestream.yml -level all /data/mealquest.db

# b. Restore the replica to a scratch file beside the live database (the
#    live files are not touched) and move it off the volume; then take a
#    consistent snapshot of the live database next to it.
docker compose -f deploy/compose.yaml run --rm --no-deps litestream \
  restore -config /etc/litestream.yml -o /data/predeploy-check.db /data/mealquest.db
sudo sh -c "mv $D/predeploy-check.db* /tmp/mqcheck/"
sudo python3 /tmp/mqsnap.py "$D/mealquest.db" /tmp/mqcheck/live.db
sudo chown -R "$USER" /tmp/mqcheck
sudo ls -ln "$D"   # mealquest.db-wal and -shm owned by 10001, not 0: see "Count check"

# c. Count check both: the restored replica must report integrity ok and
#    exactly the live snapshot's counts and content-sha256.
python3 /tmp/mqcount.py /tmp/mqcheck/predeploy-check.db > /tmp/mqcheck/restored.txt
python3 /tmp/mqcount.py /tmp/mqcheck/live.db > /tmp/mqcheck/live.txt
cat /tmp/mqcheck/restored.txt
grep -qx 'integrity ok' /tmp/mqcheck/restored.txt || fail "the restored replica failed integrity_check"
diff /tmp/mqcheck/restored.txt /tmp/mqcheck/live.txt || fail "the restored replica differs from the live snapshot"

# d. The pre-deploy restore point: now, after every check passed, before
#    the restart. It is printed on the way out, once the clean-ups succeed.
point=$(date -u +%Y-%m-%dT%H:%M:%SZ)
)
```

   The verification runs in a subshell that stops at the first failed
   step. On the way out it always attempts both clean-ups — removing
   `/tmp/mqcheck` and any `predeploy-check.db` left on the volume — and a
   failed clean-up fails the verification too. The restore point is
   printed last, and only when every check and both clean-ups succeeded.
   Read it like this:

   - `pre-deploy restore point:` followed by a UTC time, and `echo $?`
     prints `0` — the backup is verified and cleaned up. Note the time for
     rollback, with the previous SHA, and go on to step 3.
   - `verification FAILED`, any other error, or a nonzero `echo $?` — no
     restore point was printed. If only the counts differed, a write
     landed between the restore and the snapshot: run the block again. If
     a clean-up failed, remove what it names by hand before running the
     block again.
   - The `ls` in step b shows `mealquest.db-wal` or `-shm` owned by `0`
     — stop, as in "Count check", even if the rest passed.

   **If any check fails, do not restart.** Production stays on the
   previous SHA (only the new image was built). Restore replication first
   — `docker compose -f deploy/compose.yaml up -d --force-recreate
   litestream`, then read its log for the cause (NAS reachable, key,
   host key) — and repeat this step until it passes. Never deploy a
   release on an unverified backup.

3. Restart and check the deployed release.

```sh
cd /home/overseer/mealquest && (
set -eu   # any failed step ends the check with a nonzero status
fail() { echo "$*" >&2; exit 1; }
sha=
# On the way out, print the deployed SHA only if every check passed.
trap 'st=$?
if [ "$st" -eq 0 ] && [ -n "$sha" ]; then echo "deployed SHA: $sha"
else [ "$st" -ne 0 ] || st=1; echo "deploy FAILED (status $st): no SHA is deployed" >&2; fi
exit "$st"' EXIT
sudo systemctl restart mealquest    # applies the firewall, then down + up -d
docker compose -f deploy/compose.yaml ps
for s in mealquest litestream searxng; do
  id=$(docker compose -f deploy/compose.yaml ps --status running --quiet "$s") || fail "docker compose ps failed"
  [ -n "$id" ] || fail "$s is not running"
done
curl -fsS http://10.60.1.14:8090/health
code=$(curl -sS -o /dev/null -w "%{http_code}" https://mealquest.cubecraftlabs.com/health)
[ "$code" = 200 ] || fail "the public /health returned $code, not 200"
sha=$(git rev-parse HEAD)   # the checked-out SHA, now running and healthy
)
```

   The check stops at the first failed step and prints the SHA last, only
   when the restart, all three services running and both health checks
   succeeded. `restart` returns once `mealquest` is healthy (Litestream
   waits for it); SearXNG may still show `health: starting` for its first
   15 seconds.

   - `deployed SHA:` followed by the SHA, and `echo $?` prints `0` —
     record that SHA, the previous SHA, the pre-deploy restore point and
     the date in the Maestro task for the release.
   - `deploy FAILED`, any other error, or a nonzero `echo $?` — record
     nothing as deployed: the release is not running healthy, and after a
     failed restart the stack may be down. Read
     `journalctl -u mealquest` and `docker compose -f deploy/compose.yaml
     logs`, then fix the cause or roll back ("Rollback to a previous SHA").

Migrations run at start-up; the service refuses to start if the database
has an applied migration it does not know.

If `deploy/.env.example` gained a variable, add it to `deploy/.env` before
the restart; the service fails fast at boot naming any missing required variable.

### Rollback to a previous SHA

First check whether the rollback crosses a migration, with the rule the
service applies at start-up (`internal/store/migrate.go`): the older build
refuses a database holding a version in `schema_migrations` that is not one
of its own migration files — the `*.sql` files directly in `migrations/`
(`migrations/queries/` holds sqlc queries, not migrations). Compare the
versions a consistent snapshot of the live database has applied with the
files the previous SHA has. First write `/tmp/mqsnap.py` with the `cat`
command in "Count check", then:

```sh
cd /home/overseer/mealquest && (
set -eu   # any failed step ends the check with a nonzero status
D=/var/lib/docker/volumes/mealquest-data/_data
P=<previous SHA>
# On the way out, remove /tmp/mqcheck whatever happens, keep any failure,
# and report every nonzero exit.
trap 'st=$?
rm -rf /tmp/mqcheck || st=1
if [ "$st" -ne 0 ]; then echo "check FAILED (status $st): stop and fix the cause" >&2; fi
exit "$st"' EXIT
rm -rf /tmp/mqcheck
mkdir /tmp/mqcheck
sudo python3 /tmp/mqsnap.py "$D/mealquest.db" /tmp/mqcheck/mealquest.db
sudo chown -R "$USER" /tmp/mqcheck
git ls-tree --name-only "$P" migrations/ > /tmp/mqcheck/known   # top level only
python3 - /tmp/mqcheck/mealquest.db /tmp/mqcheck/known <<'PY'
import sqlite3, sys
applied = {v for (v,) in sqlite3.connect(sys.argv[1]).execute("select version from schema_migrations")}
known = {n[len("migrations/"):-len(".sql")] for n in open(sys.argv[2]).read().split() if n.endswith(".sql")}
if not applied or not known:
    sys.exit("no versions read: check the snapshot and the previous SHA, then run again")
unknown = sorted(applied - known)
print("unknown to the previous SHA:", " ".join(unknown) if unknown else "none")
PY
)
```

The check runs in a subshell that stops at the first failed step — the
snapshot or its integrity check, `git ls-tree` or the comparison. On the
way out it always attempts to remove `/tmp/mqcheck`, and a failed removal
fails the check too; every nonzero exit prints `check FAILED`. Read it
like this:

- `unknown to the previous SHA: none` and `echo $?` prints `0` — no
  migration crossed: take the plain restart below.
- `unknown to the previous SHA:` followed by versions, and `echo $?`
  prints `0` — the older build does not know them: the migration-crossing
  path below is required.
- `check FAILED`, any other error, or a nonzero `echo $?` — stop and fix
  the cause, then run the check again. Do not pick a path, even if an
  `unknown to the previous SHA:` line was printed before it. If the
  clean-up failed, remove `/tmp/mqcheck` by hand first.

**No migration crossed** — rebuild and restart the older SHA:

```sh
cd /home/overseer/mealquest && (
set -eu   # any failed step ends the rollback with a nonzero status
P=<previous SHA>
fail() { echo "$*" >&2; exit 1; }
sha=
# On the way out, print the running SHA only if every check passed.
trap 'st=$?
if [ "$st" -eq 0 ] && [ -n "$sha" ]; then echo "rolled back to: $sha"
else [ "$st" -ne 0 ] || st=1; echo "rollback FAILED (status $st)" >&2; fi
exit "$st"' EXIT
git checkout --detach "$P"
docker compose -f deploy/compose.yaml build
sudo systemctl restart mealquest
docker compose -f deploy/compose.yaml ps
for s in mealquest litestream searxng; do
  id=$(docker compose -f deploy/compose.yaml ps --status running --quiet "$s") || fail "docker compose ps failed"
  [ -n "$id" ] || fail "$s is not running"
done
curl -fsS http://10.60.1.14:8090/health
sha=$(git rev-parse HEAD)
)
```

Each step runs only if the one before it succeeded, so the health
check answers only for the older SHA. Only `rolled back to:` followed by
the SHA, with `echo $?` printing `0`, means the rollback is done. On
`rollback FAILED` or a nonzero `echo $?` it is not: the first error names
the failed step, and if it came before the restart, production is still
on the release being rolled back from.

**A migration crossed** — the older binary refuses the newer schema, so the
database is restored first and the older SHA started after. Changes made
since the pre-deploy restore point are lost; say so to the household.

1. Check out and build the older SHA. Nothing restarts yet.

```sh
git checkout --detach <previous SHA> &&
docker compose -f deploy/compose.yaml build
```

   If `echo $?` then prints anything but `0`, stop: the database has not
   been touched and nothing has restarted.

2. Run "Restore the database" steps 1–4, adding
   `-timestamp <pre-deploy restore point>` (recorded in step 2 of
   "Deploy") to the `restore` command. Its step 5 then starts the older
   SHA and checks health.

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

To restore media, follow "Restore photos (media)" — never copy back while
the timer or the service can still run.

## Restore the database

SQLite in WAL mode keeps recent commits in `mealquest.db-wal`, indexed by
`mealquest.db-shm`. **A restored database must never sit beside an old WAL
or shared-memory file**: on open, SQLite would replay the old WAL's pages
onto the restored database and corrupt it or mix in state from the
database it replaced. So all three files are moved aside together, with
Litestream's local state directory `.mealquest.db-litestream`, before the
restore — and the restore runs into an empty volume.

Steps 1–3:

```sh
cd /home/overseer/mealquest && (
set -eu   # any failed step ends the restore with a nonzero status
D=/var/lib/docker/volumes/mealquest-data/_data
A=/var/tmp/mealquest-restore-aside-$(date +%Y%m%d%H%M)
fail() { echo "$*" >&2; exit 1; }
# On the way out, name the aside directory; on any failure the stack
# stays stopped.
trap 'st=$?
if [ "$st" -eq 0 ]; then echo "restored mealquest.db; the old files are in $A: run step 4"
else echo "restore FAILED (status $st): leave the stack stopped; anything moved aside is in $A" >&2; fi
exit "$st"' EXIT

# 1. Stop the whole stack (app and Litestream). Nothing may hold the
#    database open; a clean close checkpoints the WAL into mealquest.db.
sudo systemctl stop mealquest

# 2. Move the database, its -wal and -shm files and Litestream's state
#    aside — all of them, every time. Keep the directory until the
#    restored stack is verified.
sudo mkdir "$A"
for f in mealquest.db mealquest.db-wal mealquest.db-shm .mealquest.db-litestream; do
  if sudo test -e "$D/$f"; then sudo mv "$D/$f" "$A/"; fi
done
left=$(sudo ls -A "$D")
[ "$left" = media ] || fail "the volume must hold only media/, not: $left"

# 3. Restore from the NAS. --no-deps keeps the app stopped so it cannot
#    create a fresh, empty database first.
docker compose -f deploy/compose.yaml run --rm --no-deps litestream \
  restore -config /etc/litestream.yml -o /data/mealquest.db /data/mealquest.db
left=$(sudo ls -A "$D")
[ "$left" = "$(printf 'mealquest.db\nmedia')" ] || fail "the volume must hold only mealquest.db and media/ (no -wal, no -shm), not: $left"
)
```

To restore to a point in time, add `-timestamp 2026-10-05T23:00:00Z` (or
`-txid`) to the `restore` command.

The block stops at the first failed step. Only `restored mealquest.db`
with `echo $?` printing `0` lets you go on; on `restore FAILED` leave the
stack stopped, fix the cause and run the block again (a new aside
directory is made; the earlier one keeps what it holds).

4. Check the restored file before starting the app: run the copy block
   in "Count check".

5. Start the stack and check health, only after step 4 passed:

```sh
cd /home/overseer/mealquest && (
set -eu   # any failed step ends the check with a nonzero status
fail() { echo "$*" >&2; exit 1; }
sha=
# On the way out, print the running SHA only if every check passed.
trap 'st=$?
if [ "$st" -eq 0 ] && [ -n "$sha" ]; then echo "stack running and healthy at $sha"
else [ "$st" -ne 0 ] || st=1; echo "start FAILED (status $st)" >&2; fi
exit "$st"' EXIT
sudo systemctl start mealquest
docker compose -f deploy/compose.yaml ps
for s in mealquest litestream searxng; do
  id=$(docker compose -f deploy/compose.yaml ps --status running --quiet "$s") || fail "docker compose ps failed"
  [ -n "$id" ] || fail "$s is not running"
done
curl -fsS http://10.60.1.14:8090/health
sha=$(git rev-parse HEAD)
)
```

   Only `stack running and healthy at` with `echo $?` printing `0` ends
   the restore; then the aside directory may go.

### Count check

`/tmp/mqcount.py` prints row counts for key tables, the number of
application tables, a SHA-256 over every application row (first 16 hex
digits; Litestream's own `_litestream*` tables are excluded) and
`PRAGMA integrity_check`. Run it on a **copy** of the database, never on the
live file.

While the app or Litestream has the database open, the copy is a snapshot
from `/tmp/mqsnap.py`: it opens the live database read-only, copies it
with SQLite's online backup API (one consistent read, whatever is still in
the WAL included) and exits nonzero unless the snapshot's
`PRAGMA integrity_check` is `ok`. Never copy `mealquest.db` and its `-wal`
as files while they are open: a checkpoint between the two copies leaves
an older state that still reads cleanly. "Deploy" step 2 and "Rollback to
a previous SHA" use the snapshot. With the stack stopped, as in "Restore
the database" step 4, the files are closed and checkpointed, and a plain
copy is consistent (a read-only open there would leave a `-wal` and
`-shm` beside the restored file).

`mode=ro` stops writes through the snapshot's own connection, but SQLite
can still create a missing `-wal` or `-shm` beside the database, and under
`sudo` it would be owned by root. The live snapshot therefore expects the
app and Litestream to be running and holding the existing
`mealquest.db-wal` and `mealquest.db-shm`, owned by `10001`. If
`sudo ls -ln "$D"` after a live snapshot shows either sidecar owned by
`0` — the snapshot created it — stop before going further: check
`docker compose -f deploy/compose.yaml ps` (mealquest and litestream
running) and, with the cause understood, return the sidecars to
`10001:10001` before proceeding.

`/tmp` is temporary, so write the scripts before every check:

```sh
cat > /tmp/mqcount.py <<'EOF' &&
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
EOF
cat > /tmp/mqsnap.py <<'EOF'
import os, sqlite3, sys
src, dst = sys.argv[1], sys.argv[2]
if os.path.exists(dst):
    sys.exit(f"{dst} already exists")
s = sqlite3.connect(f"file:{src}?mode=ro", uri=True)
d = sqlite3.connect(dst)
s.backup(d)
s.close()
result = d.execute("pragma integrity_check").fetchall()
d.close()
if result != [("ok",)]:
    sys.exit(f"snapshot integrity_check failed: {result}")
print("snapshot", dst, "integrity ok")
EOF
```

The second file is written only if the first was; `echo $?` must then
print `0` before either script is used.

With the stack stopped, copy the database and run it:

```sh
cd /home/overseer/mealquest && (
set -eu   # any failed step ends the check with a nonzero status
D=/var/lib/docker/volumes/mealquest-data/_data
fail() { echo "$*" >&2; exit 1; }
# On the way out, remove /tmp/mqcheck whatever happens, keep any failure,
# and give the verdict only if every step and the clean-up succeeded.
trap 'st=$?
rm -rf /tmp/mqcheck || st=1
if [ "$st" -eq 0 ]; then echo "count check: integrity ok"
else echo "count check FAILED (status $st): do not start the stack" >&2; fi
exit "$st"' EXIT
rm -rf /tmp/mqcheck
mkdir /tmp/mqcheck
sudo sh -c "cp -a $D/mealquest.db* /tmp/mqcheck/"
sudo chown -R "$USER" /tmp/mqcheck
python3 /tmp/mqcount.py /tmp/mqcheck/mealquest.db > /tmp/mqcheck/counts.txt
cat /tmp/mqcheck/counts.txt
grep -qx 'integrity ok' /tmp/mqcheck/counts.txt || fail "the copy failed integrity_check"
)
```

The check stops at the first failed step — the clean-up of an earlier
`/tmp/mqcheck`, the copy, the count or the integrity check — so it never
reads a file left by an earlier run. Only `count check: integrity ok`
with `echo $?` printing `0` passes; then the counts printed above it,
before and after, must match exactly.

## Restore photos (media)

Recipe and dinner photos live in `media/` in the data volume and are
backed up nightly to `joshua@10.60.1.50:/volume1/homes/Joshua/mealquest-backups/media/`
(see "Media — nightly rsync"). The backup service mirrors with
`rsync --delete`. **An `rsync --delete` run must never see an empty or
partial `media/`**: it would delete the NAS copy you are about to restore
from. Stopping the timer is not enough — it does not stop a run that is
already in flight, and the service can still be started by hand. So stop
**both** the timer and the service, confirm no rsync is running, and do
not start the service until the restore is verified.

Run from `/home/overseer/mealquest`:

```sh
cd /home/overseer/mealquest && (
set -eu   # any failed step ends the restore with a nonzero status
M=/var/lib/docker/volumes/mealquest-data/_data/media
NAS=/volume1/homes/Joshua/mealquest-backups/media
SSH="ssh -i deploy/secrets/litestream_key -o IdentitiesOnly=yes -o UserKnownHostsFile=/etc/mealquest/known_hosts -o StrictHostKeyChecking=yes"
fail() { echo "$*" >&2; exit 1; }
nas= here= w=
# On the way out, give the counts only if every step passed; on any
# failure, report the timer's actual state, not an assumed one.
trap 'st=$?
[ -z "$w" ] || rm -rf "$w"
if [ "$st" -eq 0 ] && [ -n "$here" ]; then echo "media restored: $here files here, $nas on the NAS, all $nas match the NAS manifest"
else [ "$st" -ne 0 ] || st=1; t=$(systemctl is-active mealquest-media-backup.timer) || :
  if [ "$t" = inactive ]; then echo "media restore FAILED (status $st): the timer is inactive; leave it stopped and find out why" >&2
  else echo "media restore FAILED (status $st): the timer is ${t:-unknown}, not inactive; run sudo systemctl stop mealquest-media-backup.timer mealquest-media-backup.service and confirm it is inactive before anything else" >&2; fi; fi
exit "$st"' EXIT
w=$(mktemp -d)

# 1. Stop the nightly mirror: the timer AND any run in flight.
sudo systemctl stop mealquest-media-backup.timer mealquest-media-backup.service

# 2. Pre-check — all must hold before media/ is touched. is-active exits
#    nonzero for every state but active, so its printed state is checked;
#    pgrep exits 0 on a match, 1 on none, 2+ on an error.
ts=$(systemctl is-active mealquest-media-backup.timer) || :
[ "$ts" = inactive ] || fail "the timer is ${ts:-unknown}, not inactive"
ss=$(systemctl is-active mealquest-media-backup.service) || :
case "$ss" in
  inactive|failed) ;;
  *) fail "the service is ${ss:-unknown}, not inactive or failed";;
esac
ps=0
pgrep -a -u mealquest rsync || ps=$?
case "$ps" in
  0) fail "an rsync is still running as mealquest";;
  1) ;;   # no match: no rsync
  *) fail "pgrep failed (status $ps)";;
esac
l=$(sudo $SSH joshua@10.60.1.50 "find $NAS -type f") || fail "listing the NAS copy failed"
[ -n "$l" ] || fail "the NAS copy holds no files"
nas=$(printf '%s\n' "$l" | wc -l)   # the target below
# The NAS manifest: one "sha256  ./path" line per file, taken now, before
# the copy. Every check below compares content against it, not counts.
sudo $SSH joshua@10.60.1.50 "cd $NAS && find . -type f -exec sha256sum {} +" >"$w/raw" || fail "hashing the NAS copy failed"
LC_ALL=C sort "$w/raw" >"$w/nas"
[ "$(wc -l <"$w/nas")" -eq "$nas" ] || fail "the NAS manifest has $(wc -l <"$w/nas") files, the listing $nas"

# 3. Stop the app (no writes into media/ meanwhile).
docker compose -f deploy/compose.yaml stop mealquest

# 4. Recreate the directory if it is gone, owned by the app user.
sudo install -d -o 10001 -g 10001 -m 0755 "$M"

# 5. Copy back from the NAS as uid 10001's files. No --delete in this
#    direction: the NAS copy is the source and is only read. --checksum
#    compares content, so a local file with the NAS size and mtime but
#    other bytes is still replaced.
sudo rsync -a --checksum --stats --chown=10001:10001 -e "$SSH" "joshua@10.60.1.50:$NAS/" "$M/"

# 6. Post-condition — before anything can mirror again: at least the NAS
#    count here, and the NAS count unchanged.
l=$(sudo find "$M" -type f) || fail "listing $M failed"
[ -n "$l" ] || fail "$M holds no files after the copy"
n=$(printf '%s\n' "$l" | wc -l)
[ "$n" -ge "$nas" ] || fail "only $n of the NAS's $nas files are here: the copy did not complete"
l=$(sudo $SSH joshua@10.60.1.50 "find $NAS -type f") || fail "listing the NAS copy failed"
[ -n "$l" ] && [ "$(printf '%s\n' "$l" | wc -l)" -eq "$nas" ] || fail "the NAS file count changed from $nas"
# Content: every file in the NAS manifest is here with the same bytes,
# and the NAS still matches the manifest taken before the copy.
sudo sh -c 'cd "$1" && find . -type f -exec sha256sum {} +' sh "$M" >"$w/raw" || fail "hashing $M failed"
LC_ALL=C sort "$w/raw" >"$w/here"
LC_ALL=C comm -23 "$w/nas" "$w/here" >"$w/bad"
[ ! -s "$w/bad" ] || { cat "$w/bad" >&2; fail "$(wc -l <"$w/bad") NAS file(s) above are missing here or differ in content"; }
sudo $SSH joshua@10.60.1.50 "cd $NAS && find . -type f -exec sha256sum {} +" >"$w/raw" || fail "hashing the NAS copy failed"
LC_ALL=C sort "$w/raw" | cmp -s - "$w/nas" || fail "the NAS copy changed during the restore"
here=$n

# 7. Start the app, then the timer again. Only now may the service run.
docker compose -f deploy/compose.yaml up -d
curl -fsS http://10.60.1.14:8090/health
sudo systemctl start mealquest-media-backup.timer
systemctl is-active mealquest-media-backup.timer   # active; exits nonzero otherwise
)
```

The block stops at the first failed step and gives the counts last, only
when every step passed and every file in the NAS manifest (a SHA-256 of
each file, taken before the copy) is present locally with the same bytes.
On `media restore FAILED` or a nonzero `echo $?` the message gives the
timer's actual state: if it is not `inactive`, stop it as the message says
before anything else; then leave it stopped. The local count may exceed the NAS count only if
`media/` still held files the NAS never received (photos added since the
last nightly run); the next nightly run then copies them up. If the local count is lower,
the copy did not complete: fix the cause and run the block again.

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

The app key encrypts every stored secret: the `_ct` columns
`ai_settings.api_key_ct` (LLM API key), `recipe_search_settings.api_key_ct`
(recipe-search API key), `integrations_google.refresh_token_ct` (Google
Calendar refresh token) and `chat_settings.webhook_url_ct` (Google Chat
webhook URL). **Rotating it re-encrypts nothing**: a value encrypted with
the old key can no longer be read. A `mealquest rotate-key` helper that
re-encrypts in place is planned but does not exist yet.

**What is recoverable today.** The service currently exposes only
household settings; no shipped flow writes or re-enters any of these four
secrets. The re-entry flows are planned: the LLM key in Settings → AI
assistant (the AI settings feature), the recipe-search key (Feature 10),
Google Calendar reconnect (Feature 16) and the Google Chat webhook
(Feature 19). So today the rotation is safe **only if no `_ct` value is
stored**, which the pre-check below proves. If the pre-check finds a
stored value, do not rotate: it could not be re-entered afterwards. Keep
the current key and wait for the matching flow (or the `rotate-key`
helper).

**The pre-check and the key replacement run with the whole stack
stopped**, from the pre-check until the new key is in place. A raw copy of
`mealquest.db` and `mealquest.db-wal` taken while the stack runs is **not**
a consistent snapshot: the two files are copied one after the other, and a
checkpoint (by the app or by Litestream) between the two copies can move
committed pages out of the WAL into `mealquest.db` after `mealquest.db` was
already copied, then reset the WAL before it is copied. The copy then
lacks those commits — possibly a stored `_ct` value — and the pre-check
reports a false 0. A running app could also store a `_ct` value after a
correct pre-check and before the key is replaced. Stopping both containers
removes every writer (Litestream also writes and checkpoints), and the
clean close checkpoints the WAL into `mealquest.db`, so the file alone is
complete.

Run from `/home/overseer/mealquest`:

```sh
D=/var/lib/docker/volumes/mealquest-data/_data

# 1. Stop the whole stack (app and Litestream) and leave it stopped until
#    step 4. Nothing may hold the database open.
sudo systemctl stop mealquest
sudo ls -la "$D"   # mealquest.db and media/ only: no -wal, no -shm; if
                   # either exists, the close was not clean — stop here

# 2. Pre-check, on a copy of the now-quiescent database.
rm -rf /tmp/mqcheck && mkdir /tmp/mqcheck
sudo cp -a "$D/mealquest.db" /tmp/mqcheck/ && sudo chown -R "$USER" /tmp/mqcheck
python3 - /tmp/mqcheck/mealquest.db <<'PY'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
for t, col in (("ai_settings", "api_key_ct"), ("recipe_search_settings", "api_key_ct"),
               ("integrations_google", "refresh_token_ct"), ("chat_settings", "webhook_url_ct")):
    print(t, col, c.execute(f"select count(*) from {t} where {col} is not null").fetchone()[0])
PY
rm -rf /tmp/mqcheck
# every count must be 0; otherwise start the stack again on the current
# key (sudo systemctl start mealquest) and stop here

# 3. Replace the key, still with the stack stopped.
sudo cp -a deploy/secrets/secret_key /var/tmp/secret_key.old   # keep until verified, then shred
head -c 32 /dev/urandom | base64 | sudo tee deploy/secrets/secret_key >/dev/null
sudo chown 10001:10001 deploy/secrets/secret_key
sudo chmod 600 deploy/secrets/secret_key

# 4. Start the stack on the new key and check health.
sudo systemctl start mealquest
docker compose -f deploy/compose.yaml ps
curl -fsS http://10.60.1.14:8090/health
```

Then sign in and open household settings to confirm the service runs on
the new key. Finally `sudo shred -u /var/tmp/secret_key.old`. Once the
planned flows ship, this section gains a step per secret to re-enter it
and confirm it works.

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

### Google OAuth client secret (`deploy/secrets/google_client_secret`)

In the Google Cloud console, open the OAuth client `mealquest`, add a new
client secret, write it to the file (same owner and mode as above) and
recreate the service:

```sh
printf '%s\n' '<new Google OAuth client secret>' | sudo tee deploy/secrets/google_client_secret >/dev/null
sudo chown 10001:10001 deploy/secrets/google_client_secret
sudo chmod 600 deploy/secrets/google_client_secret
docker compose -f deploy/compose.yaml up -d --force-recreate mealquest
```

Then disable and delete the old secret in the console. A new client secret
leaves the household's stored refresh token valid, so nothing needs
reconnecting.

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

### SearXNG secret (`deploy/secrets/searxng.env`)

Only signs SearXNG's own cookies and image-proxy links; nothing stored
depends on it. Replace it and recreate the service:

```sh
(umask 077; printf 'SEARXNG_SECRET=%s\n' "$(openssl rand -hex 32)" > deploy/secrets/searxng.env)
docker compose -f deploy/compose.yaml up -d --force-recreate searxng
docker compose -f deploy/compose.yaml ps searxng   # healthy
```

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
