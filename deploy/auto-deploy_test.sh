#!/usr/bin/env bash
# Tests the CI gate of deploy/auto-deploy.sh against stubbed GitHub answers:
# no network, no docker, no host access. The gate's functions (fail,
# ci_state, ci_gate) and the deploy-record line are taken verbatim from the
# script and run with curl replaced by a function that serves fixtures for
# exactly the target SHA's check-runs and status URLs.
#
#   deploy/auto-deploy_test.sh    (also run by `make test`)
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
script=$here/auto-deploy.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

sha=273cf8fec0a82bad1b8ff2e18a6b63b0b61162ee
other=561f2490000000000000000000000000000000aa
api=https://api.github.com/repos/CubeCraft-Labs/MealQuest/commits

# The gate's functions and the verified deploy's record line, from the script.
{
  grep -E '^fail\(\) ' "$script"
  awk '/^(ci_state|ci_gate)\(\) \{$/,/^\}$/' "$script"
} > "$tmp/gate.sh"
grep -q '^ci_gate() {' "$tmp/gate.sh" && grep -q '^ci_state() {' "$tmp/gate.sh" \
  || { echo "could not extract ci_state and ci_gate from $script" >&2; exit 1; }
record_line=$(grep -F 'previous=$rollback deployed=$deployed restore_point=$point' "$script")
[ "$(grep -c . <<< "$record_line")" = 1 ] || { echo "no single deploy-record line in $script" >&2; exit 1; }

# run_gate <fixture dir> <opt-in value or "unset">: runs ci_gate "$sha" and,
# when it passes, the script's deploy-record line, as the script would.
run_gate() {
  local fix=$1 optin=$2
  (
    unset MEALQUEST_DEPLOY_ALLOW_NO_CI
    [ "$optin" = unset ] || export MEALQUEST_DEPLOY_ALLOW_NO_CI=$optin
    FORGE_API=$(grep -E '^FORGE_API=' "$script" | cut -d = -f 2-)
    RECORD=$fix/record
    curl() {
      local url=${*: -1}
      case "$url" in
        "$api/$sha/check-runs?per_page=100") [ -f "$fix/check-runs.json" ] && cat "$fix/check-runs.json" ;;
        "$api/$sha/status?per_page=100") [ -f "$fix/status.json" ] && cat "$fix/status.json" ;;
        *) echo "unexpected URL in test: $url" >&2; return 99 ;;
      esac || { echo "curl: (22) The requested URL returned error: 503" >&2; return 22; }
    }
    set -euo pipefail
    # shellcheck source=/dev/null
    source "$tmp/gate.sh"
    ci=unset
    ci_gate "$sha"
    echo "PROCEED ci=$ci"
    rollback=$other deployed=$sha point=2026-10-25T12:00:00Z
    eval "$record_line"
  )
}

# fixture <name> <check runs JSON array> <statuses JSON array> [head_sha]
# [check-runs total_count]: writes the forge's two answers for $sha.
fixture() {
  local dir=$tmp/$1 head=${4:-$sha}
  mkdir -p "$dir"
  python3 -I -c '
import json, sys
runs, statuses, sha, head = json.loads(sys.argv[1]), json.loads(sys.argv[2]), sys.argv[3], sys.argv[4]
total = int(sys.argv[5]) if sys.argv[5] else len(runs)
for r in runs:
    r["head_sha"] = head
json.dump({"total_count": total, "check_runs": runs}, open(sys.argv[6] + "/check-runs.json", "w"), indent=2)
json.dump({"state": "pending" if not statuses else "x", "sha": head, "total_count": len(statuses), "statuses": statuses}, open(sys.argv[6] + "/status.json", "w"), indent=2)
' "$2" "$3" "$sha" "$head" "${5:-}" "$dir"
}
ok='{"name":"build","status":"completed","conclusion":"success"}'
ok2='{"name":"test","status":"completed","conclusion":"success"}'

fixture failing-check "[$ok, {\"name\":\"test\",\"status\":\"completed\",\"conclusion\":\"failure\"}]" '[]'
fixture failing-status "[$ok]" '[{"context":"ci/legacy","state":"error"}]'
fixture all-green "[$ok, $ok2]" '[{"context":"ci/legacy","state":"success"}]'
fixture pending "[$ok, {\"name\":\"test\",\"status\":\"in_progress\",\"conclusion\":null}]" '[]'
fixture skipped "[$ok, {\"name\":\"test\",\"status\":\"completed\",\"conclusion\":\"skipped\"}]" '[]'
fixture no-checks '[]' '[]'
fixture other-commit "[$ok]" '[]' "$other"
fixture truncated "[$ok]" '[]' "$sha" 101
mkdir -p "$tmp/unreachable"

# name | fixture | opt-in | exit | PROCEED ci= (or "-": must not proceed) | output must contain
cases=(
  "failing check aborts|failing-check|unset|1|-|CI for origin/main $sha is failure test=failure, not success: not deploying"
  "failing check aborts even with the opt-in|failing-check|1|1|-|is failure test=failure"
  "failing commit status aborts|failing-status|unset|1|-|is failure ci/legacy=error"
  "skipped check is not success and aborts|skipped|unset|1|-|is failure test=skipped"
  "all checks successful proceeds|all-green|unset|0|success|CI for $sha on GitHub: success 3 checks"
  "pending check waits without deploying|pending|unset|0|-|"
  "no configured checks aborts without the opt-in|no-checks|unset|1|-|GitHub reports no CI for origin/main $sha (no check runs, no commit statuses): not deploying without forge-verified CI. Add CI on GitHub, or set MEALQUEST_DEPLOY_ALLOW_NO_CI=1"
  "no configured checks proceeds with the opt-in and logs it|no-checks|1|0|none(MEALQUEST_DEPLOY_ALLOW_NO_CI=1)|MEALQUEST_DEPLOY_ALLOW_NO_CI=1 is set: deploying WITHOUT forge-verified CI"
  "an answer for another commit aborts|other-commit|unset|1|-|GitHub answered for a commit other than $sha"
  "a partial check list aborts|truncated|unset|1|-|GitHub listed 1 of 101 check runs"
  "an unreadable forge aborts|unreachable|unset|1|-|could not read the CI of $sha from $api: not deploying"
)

failures=0
for c in "${cases[@]}"; do
  IFS='|' read -r name fix optin want_exit want_ci want_out <<< "$c"
  st=0
  out=$(run_gate "$tmp/$fix" "$optin" 2>&1) || st=$?
  problems=()
  [ "$st" = "$want_exit" ] || problems+=("exit $st, want $want_exit")
  if [ "$want_ci" = - ]; then
    ! grep -q '^PROCEED' <<< "$out" || problems+=("proceeded to deploy")
    [ ! -e "$tmp/$fix/record" ] || problems+=("wrote a deploy record")
  else
    grep -qxF "PROCEED ci=$want_ci" <<< "$out" || problems+=("did not proceed with ci=$want_ci")
    grep -qxE "[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z previous=$other deployed=$sha restore_point=2026-10-25T12:00:00Z ci=$(sed 's/[()]/\\&/g' <<< "$want_ci")" "$tmp/$fix/record" 2>/dev/null \
      || problems+=("deploy record lacks ci=$want_ci: $(cat "$tmp/$fix/record" 2>/dev/null)")
  fi
  if [ -n "$want_out" ]; then
    grep -qF -- "$want_out" <<< "$out" || problems+=("output lacks: $want_out")
  elif [ "$want_ci" = - ] && [ "$want_exit" = 0 ]; then
    [ -z "$out" ] || problems+=("pending printed output")
  fi
  rm -f "$tmp/$fix/record"
  if [ "${#problems[@]}" -eq 0 ]; then
    echo "PASS  $name"
  else
    failures=$((failures + 1))
    echo "FAIL  $name: ${problems[*]}"
    sed 's/^/      | /' <<< "$out"
  fi
done

# The real script refuses an opt-in value other than 1 before doing anything.
name="an opt-in value other than 1 is refused at start"
st=0
out=$(MEALQUEST_DEPLOY_ALLOW_NO_CI=yes bash "$script" 2>&1) || st=$?
if [ "$st" = 1 ] && grep -qF 'MEALQUEST_DEPLOY_ALLOW_NO_CI is "yes": set it to 1 (deploy without forge-verified CI) or leave it unset' <<< "$out"; then
  echo "PASS  $name"
else
  failures=$((failures + 1)); echo "FAIL  $name: exit $st: $out"
fi

# The retired Gitea host is gone from the script.
name="the script calls no Gitea host"
if grep -qiE 'gitea|code\.cubecraftlabs\.com' "$script"; then
  failures=$((failures + 1)); echo "FAIL  $name: $(grep -niE 'gitea|code\.cubecraftlabs\.com' "$script")"
else
  echo "PASS  $name"
fi

[ "$failures" -eq 0 ] || { echo "$failures auto-deploy gate test(s) failed" >&2; exit 1; }
echo "auto-deploy gate: all tests passed"
