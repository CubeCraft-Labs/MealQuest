# MealQuest deploy runbook

The one way to deploy MealQuest to production: the docker compose project
`mealquest` on office-srvr `10.60.1.14` (see
[ARCHITECTURE.md](../docs/ARCHITECTURE.md#topology)). Production runs `main`
or a release tag; only `overseer` merges `dev` → `main`. Every deploy builds
on the host, from the checked-out commit, with `make docker`.

Host setup (clone, firewall, the systemd unit, Traefik, DNS, Litestream) and
the database restore are in CTXD project `mealquest`, `RUNBOOKS.MD`. This
file is the build, restart, verification and rollback procedure; where it
uses a CTXD section, it names it.

All commands run on `10.60.1.14` as `overseer`, in bash, from the checkout
`/home/overseer/mealquest`. A block that ends with `FAILED` or a non-zero
`$?` stops the deploy: do not go on to the next step.

## How the image carries its version

`make docker` runs `docker build --build-arg VERSION=<version>` and tags the
image `mealquest:<version>`, where `<version>` is
`git describe --tags --always` of the checkout (a release tag such as
`v1.0.0`, otherwise the short SHA), with `-dirty` added when a tracked file
is modified. The [Dockerfile](../Dockerfile) links it into the binary, and
`/health` reports it:

```json
{"status":"ok","version":"v1.0.0"}
```

Never pass `--build-arg VERSION` by hand, and never build with
`docker compose build` or `docker compose up --build`: compose passes no
version, so that image reports `dev`.

The compose service `mealquest` runs the image named `mealquest-mealquest`
(compose's name for the image it would build; check with
`docker compose -f deploy/compose.yaml config --images mealquest`). It
already exists, so `docker compose up` uses it and never builds. A deploy
points that name at the `make docker` image with `docker tag`. Every
`mealquest:<version>` image is kept for rollback; do not prune them.

## Prerequisites

- `make`, `git`, `curl`, `python3` and Docker Engine with Compose on the
  host (`make` is not in the base install: `sudo apt-get install make`).
- `deploy/.env` (git-ignored, owner `overseer`, mode 0600) sets every
  variable [`deploy/.env.example`](.env.example) sets, with
  `MEALQUEST_BIND_ADDRESS=10.60.1.14`. It must not set the variables
  `deploy/compose.yaml` fixes (`MEALQUEST_HTTP_ADDR`, `MEALQUEST_DATA_DIR`,
  `MEALQUEST_SEARXNG_URL` and the `*_FILE` variables). Creating it: CTXD
  `RUNBOOKS.MD` "Stack and first deploy" > "3. Environment file".
- `deploy/secrets/` (git-ignored, mode 0700) holds `secret_key`,
  `oidc_client_secret`, `google_client_secret`, `vapid_private_key` and
  `litestream_key`, each owned by `10001:10001` with mode 0600, and
  `searxng.env`, owned by `overseer` with mode 0600. Creating them: CTXD
  `RUNBOOKS.MD` "Stack and first deploy" > "4. Secrets".
- The compose file is the checkout's [`deploy/compose.yaml`](compose.yaml);
  the systemd unit [`mealquest.service`](systemd/mealquest.service) starts
  and stops it and is the only way the stack starts.
- DNS and Traefik already route `https://mealquest.cubecraftlabs.com` to
  `10.60.1.14:8090` (CTXD `RUNBOOKS.MD` "Reverse proxy (Traefik)", "DNS").

Check `deploy/.env` and the secrets before every deploy (a new release may
add a variable; the service refuses to boot without it). Both commands must
print nothing:

```sh
cd /home/overseer/mealquest
comm -23 <(grep -oE '^MEALQUEST_[A-Z_]+' deploy/.env.example | sort -u) \
         <(grep -oE '^MEALQUEST_[A-Z_]+' deploy/.env | sort -u)   # variables missing from deploy/.env
sudo find deploy/secrets -type f ! -perm 600                       # secrets not mode 0600
```

## Deploy

### 1. Build

Record the release production is running now, check out the new release
and build its image. Nothing restarts.

The rollback target is the version the running service reports on
`/health`, read before the checkout changes, never the checkout's `HEAD`:
after a failed or deferred deploy the checkout can be ahead of production.
The block resolves that version to a commit and prints it as the rollback
SHA. A running version of `dev` or `*-dirty` names no commit: the block
fails, and the rollback target must be established by hand before going on.

The checkout must be clean (the same test as the Makefile's `GIT_DIRTY`:
no modified tracked file outside the generated `internal/frontend/dist`).
The block computes the image tag exactly as `make docker` does, `-dirty`
included, and checks that image exists after the build.

```sh
cd /home/overseer/mealquest && (
set -euo pipefail
fail() { echo "$*" >&2; exit 1; }
built=
trap 'st=$?
if [ "$st" -eq 0 ] && [ -n "$built" ]; then echo "built: $built"
else [ "$st" -ne 0 ] || st=1; echo "build FAILED (status $st)" >&2; fi
exit "$st"' EXIT
bind=$(grep -E '^MEALQUEST_BIND_ADDRESS=' deploy/.env | cut -d = -f 2-)
[ -n "$bind" ] || fail "deploy/.env sets no MEALQUEST_BIND_ADDRESS"
running=$(curl -fsS "http://$bind:8090/health" | python3 -c 'import json, sys; print(json.load(sys.stdin)["version"])') \
  || fail "http://$bind:8090/health did not return a version: no running release to roll back to"
case "$running" in
  dev|*-dirty) fail "the running release reports $running, which names no commit" ;;
esac
git fetch --tags origin
rollback=$(git rev-parse --verify --quiet "$running^{commit}") \
  || fail "running version $running is not a commit in this repository"
echo "rollback SHA: $rollback (running version $running)"   # RECORD it for rollback
git checkout --detach "refs/remotes/origin/main^{commit}"   # or the release tag, e.g. v1.0.0
dirty=$(git status --porcelain --untracked-files=no -- . ':(exclude)internal/frontend/dist')
[ -z "$dirty" ] || fail "the checkout has modified tracked files; clean it first:
$dirty"
v=$(git describe --tags --always)${dirty:+-dirty}   # the Makefile's VERSION
make docker
docker image inspect "mealquest:$v" > /dev/null 2>&1 || fail "no image mealquest:$v: the build failed"
built="mealquest:$v"
)
```

### 2. Ship and restart

1. **Pre-restart gates.** Run CTXD `RUNBOOKS.MD` "Routine deploy and
   rollback" > "Deploy" step 2 (a verified pre-deploy restore point; record
   it with the rollback SHA) and step 3 (boot-required configuration). The
   restart applies migrations the previous release refuses, so without that
   restore point there is no rollback. If either fails, do not restart:
   production stays on the previous release.
2. **Point the stack at the new image and restart.** `docker compose pull`
   fetches the pinned `litestream` and `searxng` images before anything
   stops; `mealquest` is never pulled. The restart applies the firewall,
   then runs `down` and `up -d`, and returns once `mealquest` passes its
   healthcheck (`/health` inside the container).

```sh
cd /home/overseer/mealquest && (
set -euo pipefail
fail() { echo "$*" >&2; exit 1; }
up=
trap 'st=$?
if [ "$st" -eq 0 ] && [ -n "$up" ]; then echo "restarted on: $up"
else [ "$st" -ne 0 ] || st=1; echo "restart FAILED (status $st)" >&2; fi
exit "$st"' EXIT
v=$(git describe --tags --always)
docker compose -f deploy/compose.yaml pull litestream searxng
docker tag "mealquest:$v" mealquest-mealquest
sudo systemctl restart mealquest
for s in mealquest litestream searxng; do
  id=$(docker compose -f deploy/compose.yaml ps --status running --quiet "$s")
  [ -n "$id" ] || fail "$s is not running"
done
id=$(docker compose -f deploy/compose.yaml ps --quiet mealquest)
health=$(docker inspect -f '{{.State.Health.Status}}' "$id")
[ "$health" = healthy ] || fail "mealquest is $health, not healthy"
up="mealquest:$v"
)
```

On failure read `journalctl -u mealquest` and
`docker compose -f deploy/compose.yaml logs mealquest`: a configuration
error names the variable. Fix it or roll back.

### 3. Verify the deployed version and a real screen

A `/health` 200 alone proves only that some build is up. This step checks,
on the LAN address and through Traefik, that:

- `/health` reports exactly the checkout's `git describe --tags --always`;
- `/` and the Plan screen's route `/plan` serve the built PWA shell (the
  React root and a hashed `/assets/` script, not the placeholder page an
  unbuilt embed serves);
- that script is served as JavaScript.

```sh
cd /home/overseer/mealquest && (
set -euo pipefail
fail() { echo "$*" >&2; exit 1; }
want=
trap 'st=$?
if [ "$st" -eq 0 ] && [ -n "$want" ]; then echo "deployed and verified: $want ($(git rev-parse HEAD))"
else [ "$st" -ne 0 ] || st=1; echo "verification FAILED (status $st): the deploy is not verified" >&2; fi
exit "$st"' EXIT
v=$(git describe --tags --always)
for base in http://10.60.1.14:8090 https://mealquest.cubecraftlabs.com; do
  got=$(curl -fsS "$base/health" | python3 -c 'import json, sys; print(json.load(sys.stdin)["version"])') \
    || fail "$base/health did not return a version"
  [ "$got" = "$v" ] || fail "$base/health reports version $got, the checkout is $v"
  for path in / /plan; do
    page=$(curl -fsS "$base$path") || fail "$base$path did not return 200"
    grep -q '<div id="root"></div>' <<< "$page" || fail "$base$path is not the PWA shell"
    js=$(grep -oE 'src="/assets/[^"]+\.js"' <<< "$page" | head -n 1 | cut -d '"' -f 2) || true
    [ -n "$js" ] || fail "$base$path references no built /assets/ script"
    type=$(curl -fsS -o /dev/null -w '%{content_type}' "$base$js") || fail "$base$js did not return 200"
    case "$type" in
      text/javascript*) ;;
      *) fail "$base$js is $type, not JavaScript" ;;
    esac
  done
  echo "$base: version $got, PWA shell and $js served"
done
want=$v
)
```

- `deployed and verified: <version> (<SHA>)` and `$?` = `0` → open
  `https://mealquest.cubecraftlabs.com` in a browser: sign in and the Home
  screen renders with the household's data. Then record the deployed
  version and SHA, the rollback SHA, the pre-deploy restore point and the
  date in the release's Maestro task.
- `verification FAILED`, any error or a non-zero `$?` → the deploy is not
  done. A version mismatch means the stack runs a different image than the
  checkout: repeat step 2, or roll back.

## Rollback to a previous release

Rolling back points `mealquest-mealquest` at the previous release's
`mealquest:<version>` image again; that release's checkout keeps the
compose file and the version check consistent with it.

1. **Check whether the rollback crosses a migration** with CTXD
   `RUNBOOKS.MD` "Routine deploy and rollback" > "Rollback to a previous
   SHA" (its first block), run against the rollback SHA that deploy step 1
   recorded. If it prints versions unknown to the rollback SHA, restore the
   database to the pre-deploy restore point first (CTXD `RUNBOOKS.MD`
   "Restore the database", with `-timestamp <restore point>`); changes
   since then are lost, so tell the household.
2. **Check out the rollback SHA and retag its image.** An image built before
   this runbook was not tagged `mealquest:<version>`; `make docker` builds
   it from the same commit.

```sh
cd /home/overseer/mealquest && (
set -euo pipefail
fail() { echo "$*" >&2; exit 1; }
P=<rollback SHA>   # recorded by step 1, from the running release's /health
back=
trap 'st=$?
if [ "$st" -eq 0 ] && [ -n "$back" ]; then echo "rolled back to: $back"
else [ "$st" -ne 0 ] || st=1; echo "rollback FAILED (status $st)" >&2; fi
exit "$st"' EXIT
git checkout --detach "$P"
v=$(git describe --tags --always)
docker image inspect "mealquest:$v" > /dev/null 2>&1 || make docker
docker image inspect "mealquest:$v" > /dev/null 2>&1 || fail "no image mealquest:$v"
docker tag "mealquest:$v" mealquest-mealquest
sudo systemctl restart mealquest
back="mealquest:$v"
)
```

3. **Run step 3 above** unchanged. The rollback is done only when it prints
   `deployed and verified:` with the previous release's version.

## Automated deploys

[`auto-deploy.sh`](auto-deploy.sh), installed as
`/usr/local/sbin/mealquest-auto-deploy`, runs this runbook's deploy unattended:
step 1 (build), the prerequisite `deploy/.env` and secrets check, CTXD
`RUNBOOKS.MD` "Deploy" steps 2 (pre-deploy restore point) and 3
(boot-required configuration), step 2 (ship and restart) and step 3
(verify). No check is skipped or weakened, and it stops at the first
failure. Every failure before the restart leaves production on the previous
release.

### What runs, and when

[`mealquest-auto-deploy.timer`](systemd/mealquest-auto-deploy.timer) starts
[`mealquest-auto-deploy.service`](systemd/mealquest-auto-deploy.service)
at every quarter hour (`OnCalendar=*-*-* *:0/15:00`), plus up to 5 minutes'
random delay; a check missed while the host was down runs as soon as it is
back (`Persistent=true`). The service runs the script as `overseer` from the
checkout, and only while `mealquest.service` is active: a stack stopped on
purpose (a database or media restore) is skipped, not redeployed. Output goes
to the journal (`journalctl -u mealquest-auto-deploy`).

Each run fetches `origin` and compares `origin/main` with the last
`deployed=` SHA in the deploy record `/home/overseer/mealquest-auto-deploy.log`:

| `origin/main` | Result |
| --- | --- |
| is the last deployed SHA | No-op: exit 0, no output. |
| CI (Gitea commit status) `pending` | Exit 0, no output; the next run checks again. |
| CI `failure`, `error`, `warning`, or no status yet | Error in the journal, exit 1, nothing deployed. |
| the last record line is `FAILED` for this SHA | Error in the journal, exit 1, nothing deployed: a failed deploy is never retried by the timer. |
| CI `success` | Deploy. |

The CI status is read from
`https://code.cubecraftlabs.com/api/v1/repos/Maniacs_Mansion/MealQuest/commits/<SHA>/status`
(public; no credential). A commit merged moments before a run can briefly
report no status, which fails that run; the next run reads it again.

A deploy appends one line to the record:

```text
2026-10-12T15:04:05Z previous=<rollback SHA> deployed=<SHA> restore_point=2026-10-12T15:01:59Z
```

`previous` is the rollback SHA (from the running release's `/health`,
never the checkout) and `restore_point` the verified pre-deploy restore
point: record both in the release's Maestro task. A failed deploy appends
`<date> FAILED target=<SHA> status=<exit status>` and logs
`auto-deploy FAILED (status <n>)` to the journal, adding whether it failed
before the restart (production untouched) or after it (read
`journalctl -u mealquest`, then fix or roll back). Either way the checkout is
left at the target commit; the running release on `/health` stays the
rollback target.

### Install on the host

The script and units are installed copies: repeat this whenever a release
changes `deploy/auto-deploy.sh` or the units. From the checkout, at a release
that contains them:

```sh
cd /home/overseer/mealquest
sudo install -m 0755 deploy/auto-deploy.sh /usr/local/sbin/mealquest-auto-deploy
sudo install -m 0644 deploy/systemd/mealquest-auto-deploy.service \
  deploy/systemd/mealquest-auto-deploy.timer /etc/systemd/system/
sudo systemctl daemon-reload
```

The timer refuses to deploy until the record holds a deploy, so seed it with
one manual deploy of the release production should run (below), then start
the timer:

```sh
sudo /usr/local/sbin/mealquest-auto-deploy --ref refs/remotes/origin/main
sudo systemctl enable --now mealquest-auto-deploy.timer
systemctl list-timers mealquest-auto-deploy.timer
```

To stop automated deploys: `sudo systemctl disable --now mealquest-auto-deploy.timer`.

### Manual override

```sh
sudo /usr/local/sbin/mealquest-auto-deploy --ref <ref>
```

deploys an explicit ref (a SHA, tag or `origin/<branch>`) through the same
blocks, bypassing the CI and no-op gates: for a roll-forward fix, a re-run
after a `FAILED` record, or seeding the record. Run as root, the script
re-executes itself as `overseer`; it holds a lock, so it never runs alongside
the timer's run. It prints `deployed and verified:` and exits 0 only when
step 3 passed.

### Rollback

Rollback is not automated: follow "Rollback to a previous release" above
and CTXD `RUNBOOKS.MD` "Routine deploy and rollback" > "Rollback to a
previous SHA", using the `previous` SHA and `restore_point` from the record.
The timer does not undo a manual rollback: `origin/main` is still the last
recorded deploy, so its runs are no-ops until `main` moves, and a new `main`
then deploys as usual. Disable the timer if the rollback must hold.
