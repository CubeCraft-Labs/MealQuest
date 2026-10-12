#!/usr/bin/env bash
# MealQuest automated deploy: deploy/RUNBOOK.md, run unattended.
#
#   mealquest-auto-deploy              deploy origin/main if it is new and CI passed
#   mealquest-auto-deploy --ref <ref>  deploy <ref> now, bypassing the CI and no-op gates
#
# Installed as /usr/local/sbin/mealquest-auto-deploy and run as overseer by
# mealquest-auto-deploy.service (deploy/systemd); install, semantics and the
# manual override: deploy/RUNBOOK.md "Automated deploys". Run as root (sudo)
# it re-executes itself as overseer.
#
# Without --ref: exit 0 with no output when origin/main is the last deployed
# SHA in the deploy record or its CI is pending; exit 1, deploying nothing,
# when its CI failed or reports no status, or when the last attempt to deploy
# that SHA failed. Every deploy runs the runbook's blocks in order and stops
# at the first failure; nothing before the restart touches production. A
# verified deploy appends one line to the deploy record:
#   <UTC date> previous=<SHA> deployed=<SHA> restore_point=<UTC date>
# a failed one appends:
#   <UTC date> FAILED target=<SHA> status=<exit status>
set -euo pipefail

CHECKOUT=/home/overseer/mealquest
RECORD=/home/overseer/mealquest-auto-deploy.log
LOCK=/home/overseer/mealquest-auto-deploy.lock
STATUS_API=https://code.cubecraftlabs.com/api/v1/repos/Maniacs_Mansion/MealQuest/commits
PUBLIC=https://mealquest.cubecraftlabs.com
D=/var/lib/docker/volumes/mealquest-data/_data
COMPOSE=(docker compose -f deploy/compose.yaml)

fail() { echo "$*" >&2; exit 1; }

if [ "$(id -u)" -eq 0 ]; then
  exec sudo -u overseer -H -- "$0" "$@"
fi
[ "$(id -un)" = overseer ] || fail "run as overseer (or root), not $(id -un)"

ref=
case "$#" in
  0) ;;
  2) [ "$1" = --ref ] && [ -n "$2" ] || fail "usage: $0 [--ref <ref>]"; ref=$2 ;;
  *) fail "usage: $0 [--ref <ref>]" ;;
esac

exec 9>> "$LOCK"
flock -n 9 || fail "another mealquest-auto-deploy run holds $LOCK"

cd "$CHECKOUT"
git fetch --quiet --tags origin || fail "git fetch origin failed"

if [ -n "$ref" ]; then
  target=$(git rev-parse --verify --quiet "$ref^{commit}") || fail "$ref is not a commit in $CHECKOUT"
else
  target=$(git rev-parse --verify origin/main)
  [ -f "$RECORD" ] || fail "no deploy record at $RECORD: seed it with one manual deploy (deploy/RUNBOOK.md \"Automated deploys\")"
  last=$(grep -oE 'deployed=[0-9a-f]{40}' "$RECORD" | tail -n 1 | cut -d = -f 2) || true
  [ -n "$last" ] || fail "$RECORD records no deploy: seed it with one manual deploy (deploy/RUNBOOK.md \"Automated deploys\")"
  [ "$target" != "$last" ] || exit 0
  if tail -n 1 "$RECORD" | grep -q " FAILED target=$target status="; then
    fail "auto-deploy of $target already failed (see $RECORD and the journal): fix it, then deploy with --ref"
  fi
  state=$(curl -fsS --max-time 30 "$STATUS_API/$target/status" \
    | python3 -c 'import json, sys; d = json.load(sys.stdin); print(d["state"] if d["total_count"] else "absent")') \
    || fail "could not read the CI status of $target from $STATUS_API"
  case "$state" in
    success) ;;
    pending) exit 0 ;;
    *) fail "CI for origin/main $target is $state, not success: not deploying" ;;
  esac
fi

echo "auto-deploy: deploying $target${ref:+ (--ref $ref)}"
restarted=
on_exit() {
  local st=$?
  if [ "$st" -ne 0 ]; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) FAILED target=$target status=$st" >> "$RECORD"
    if [ -n "$restarted" ]; then
      echo "auto-deploy FAILED (status $st) after the restart: production may be down or unverified; read journalctl -u mealquest, then fix or roll back (deploy/RUNBOOK.md)" >&2
    else
      echo "auto-deploy FAILED (status $st): production untouched" >&2
    fi
  fi
  exit "$st"
}
trap on_exit EXIT

# deploy/RUNBOOK.md "Deploy" > "1. Build": record the rollback SHA from the
# RUNNING release, check out the target, reject a dirty checkout, build.
bind=$(grep -E '^MEALQUEST_BIND_ADDRESS=' deploy/.env | cut -d = -f 2-) || fail "deploy/.env sets no MEALQUEST_BIND_ADDRESS"
[ -n "$bind" ] || fail "deploy/.env sets no MEALQUEST_BIND_ADDRESS"
running=$(curl -fsS "http://$bind:8090/health" | python3 -c 'import json, sys; print(json.load(sys.stdin)["version"])') \
  || fail "http://$bind:8090/health did not return a version: no running release to roll back to"
case "$running" in
  dev|*-dirty) fail "the running release reports $running, which names no commit" ;;
esac
rollback=$(git rev-parse --verify --quiet "$running^{commit}") \
  || fail "running version $running is not a commit in this repository"
echo "rollback SHA: $rollback (running version $running)"
git checkout --quiet --detach "$target"
dirty=$(git status --porcelain --untracked-files=no -- . ':(exclude)internal/frontend/dist')
[ -z "$dirty" ] || fail "the checkout has modified tracked files; clean it first:
$dirty"
v=$(git describe --tags --always)${dirty:+-dirty}   # the Makefile's VERSION
make docker
docker image inspect "mealquest:$v" > /dev/null 2>&1 || fail "no image mealquest:$v: the build failed"
echo "built: mealquest:$v"

# deploy/RUNBOOK.md "Prerequisites": deploy/.env sets every variable of the
# target's .env.example, and every secret is mode 0600.
missing=$(comm -23 <(grep -oE '^MEALQUEST_[A-Z_]+' deploy/.env.example | sort -u) \
                   <(grep -oE '^MEALQUEST_[A-Z_]+' deploy/.env | sort -u))
[ -z "$missing" ] || fail "deploy/.env is missing: $missing"
loose=$(sudo find deploy/secrets -type f ! -perm 600)
[ -z "$loose" ] || fail "secrets not mode 0600: $loose"

# CTXD RUNBOOKS.MD "Routine deploy and rollback" > "Deploy" step 2: a verified
# pre-deploy restore point. The scripts are CTXD "Count check"; /tmp is
# temporary, so they are written before every check.
cat > /tmp/mqcount.py <<'EOF'
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
point=$(
set -eu
point=
trap 'st=$?
rm -rf /tmp/mqcheck || st=1
sudo sh -c "rm -f $D/predeploy-check.db*" || st=1
if [ "$st" -eq 0 ] && [ -n "$point" ]; then echo "pre-deploy restore point: $point" >&2; echo "$point"
else [ "$st" -ne 0 ] || st=1; echo "verification FAILED (status $st): do not restart" >&2; fi
exit "$st"' EXIT
exec 3>&1 1>&2   # block output to the journal; only the restore point to $point
rm -rf /tmp/mqcheck
mkdir /tmp/mqcheck
# a. Replication running, no log errors (grep: 0 match, 1 none, 2+ error), newest LTX listed
id=$("${COMPOSE[@]}" ps --status running --quiet litestream) || fail "docker compose ps failed"
[ -n "$id" ] || fail "litestream is not running"
"${COMPOSE[@]}" logs --since 24h litestream > /tmp/mqcheck/litestream.log || fail "docker compose logs failed"
gs=0
grep -i error /tmp/mqcheck/litestream.log > /tmp/mqcheck/litestream.errors || gs=$?
case "$gs" in
  0) cat /tmp/mqcheck/litestream.errors; fail "litestream logged errors in the last 24h";;
  1) ;;
  *) fail "the litestream log scan failed (status $gs)";;
esac
"${COMPOSE[@]}" exec -T litestream \
  litestream ltx -config /etc/litestream.yml -level all /data/mealquest.db
# b. Restore replica to a scratch file (live files untouched), move off the volume, snapshot live
"${COMPOSE[@]}" run --rm --no-deps litestream \
  restore -config /etc/litestream.yml -o /data/predeploy-check.db /data/mealquest.db
sudo sh -c "mv $D/predeploy-check.db* /tmp/mqcheck/"
sudo python3 /tmp/mqsnap.py "$D/mealquest.db" /tmp/mqcheck/live.db
sudo chown -R "$(id -un)" /tmp/mqcheck
sudo ls -ln "$D"
# mealquest.db-wal and -shm owned by 10001, never 0 (CTXD "Count check")
root=$(sudo find "$D" -maxdepth 1 \( -name mealquest.db-wal -o -name mealquest.db-shm \) -uid 0)
[ -z "$root" ] || fail "owned by root, not 10001 (CTXD RUNBOOKS.MD \"Count check\"): $root"
# c. Restored replica: integrity ok and identical counts/content-sha256 to the live snapshot
python3 /tmp/mqcount.py /tmp/mqcheck/predeploy-check.db > /tmp/mqcheck/restored.txt
python3 /tmp/mqcount.py /tmp/mqcheck/live.db > /tmp/mqcheck/live.txt
cat /tmp/mqcheck/restored.txt
grep -qx 'integrity ok' /tmp/mqcheck/restored.txt || fail "the restored replica failed integrity_check"
diff /tmp/mqcheck/restored.txt /tmp/mqcheck/live.txt || fail "the restored replica differs from the live snapshot"
# d. Restore point: after every check, before the restart
point=$(date -u +%Y-%m-%dT%H:%M:%SZ)
exec 1>&3
)

# CTXD RUNBOOKS.MD "Routine deploy and rollback" > "Deploy" step 3:
# boot-required configuration, secrets read the way the service reads them.
[ -f deploy/.env ] || fail "missing deploy/.env"
cid=$(grep -m 1 -E '^MEALQUEST_GOOGLE_CLIENT_ID=' deploy/.env | cut -d = -f 2- | tr -d '"' | tr -d "'") \
  || fail "missing MEALQUEST_GOOGLE_CLIENT_ID in deploy/.env"
[ -n "$cid" ] || fail "missing MEALQUEST_GOOGLE_CLIENT_ID in deploy/.env"
[ -e deploy/secrets/google_client_secret ] || fail "missing deploy/secrets/google_client_secret"
[ -s deploy/secrets/google_client_secret ] || fail "deploy/secrets/google_client_secret is empty"
id=$("${COMPOSE[@]}" ps --status running --quiet mealquest) || fail "docker compose ps failed"
[ -n "$id" ] || fail "the mealquest container is not running: start it (the check reads secrets the way the service does)"
docker exec "$id" sh -c 'test -s /run/secrets/google_client_secret && test -r /run/secrets/google_client_secret' \
  || fail "google_client_secret is not readable by the service user inside the container"
echo "boot-required configuration present"

# deploy/RUNBOOK.md "2. Ship and restart".
"${COMPOSE[@]}" pull litestream searxng
docker tag "mealquest:$v" mealquest-mealquest
restarted=yes
sudo systemctl restart mealquest
for s in mealquest litestream searxng; do
  id=$("${COMPOSE[@]}" ps --status running --quiet "$s")
  [ -n "$id" ] || fail "$s is not running"
done
id=$("${COMPOSE[@]}" ps --quiet mealquest)
health=$(docker inspect -f '{{.State.Health.Status}}' "$id")
[ "$health" = healthy ] || fail "mealquest is $health, not healthy"
echo "restarted on: mealquest:$v"

# deploy/RUNBOOK.md "3. Verify the deployed version and a real screen".
for base in "http://$bind:8090" "$PUBLIC"; do
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

deployed=$(git rev-parse HEAD)
echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) previous=$rollback deployed=$deployed restore_point=$point" >> "$RECORD"
echo "deployed and verified: $v ($deployed), previous $rollback, restore point $point"
