#!/usr/bin/env bash
# Offline proof that deploy/systemd/mealquest.service recovers the
# 2026-10-31 sequence without a manual start, and that a stack stopped
# with systemctl stop stays stopped.
#
# Docker restart → Authentik not ready → mealquest-mealquest-1 exits 1
# (auth: OIDC discovery: provider answered HTTP 503) → mealquest.service
# failed → mealquest-auto-deploy ExecCondition false. Recovery is the
# stack unit's own Restart=, not a change to that condition: auto-deploy
# still runs only while the unit is active, so it cannot redeploy a stack
# an operator stopped.
#
# The state machine is systemd's. systemd.service(5): Restart=on-failure
# schedules another start after a non-zero ExecStart, and does not run
# when the service is stopped with systemctl stop or any other systemd
# stop/restart. systemd 259 src/basic/ratelimit.c ratelimit_below: a new
# start-limit window opens only when (now - begin) > StartLimitIntervalSec;
# the burst-th start is allowed (num <= burst) and the next is refused.
# A refused start does not schedule another restart, including after the
# window later expires.
#
# No systemd, docker, or network. make test runs this via deploy-test.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
unit=$here/systemd/mealquest.service
autodeploy=$here/systemd/mealquest-auto-deploy.service
compose=$here/compose.yaml
journal=$(mktemp)
trap 'rm -f "$journal"' EXIT

die() { echo "$*" >&2; exit 1; }

# Last assignment wins, as systemd does. Comments are not directives.
directive() {
  local key=$1 file=$2
  awk -v key="$key" '
    /^[[:space:]]*[#;]/ { next }
    {
      line = $0
      sub(/[[:space:]]+$/, "", line)
      sub(/[[:space:]]+#.*$/, "", line)
      if (match(line, "^[[:space:]]*" key "=")) {
        found = substr(line, RLENGTH + 1)
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", found)
      }
    }
    END { if (found != "") print found }
  ' "$file"
}

parse_seconds() {
  local v=$1
  if [[ -z "$v" ]]; then
    echo ""
    return
  fi
  if [[ "$v" =~ ^([0-9]+)s$ || "$v" =~ ^([0-9]+)$ ]]; then
    echo "${BASH_REMATCH[1]}"
    return
  fi
  echo "INVALID"
}

restart_raw=$(directive Restart "$unit" || true)
restart_mode=${restart_raw:-no}
restart_sec_raw=$(directive RestartSec "$unit" || true)
restart_sec_parsed=$(parse_seconds "$restart_sec_raw")
# systemd's own default is 100ms. Missing means "not set on the unit";
# the simulation uses 0 so a missing value cannot spin this test.
if [[ -z "$restart_sec_parsed" ]]; then
  restart_sec=0
elif [[ "$restart_sec_parsed" == INVALID ]]; then
  die "RestartSec=$restart_sec_raw is not a whole number of seconds"
else
  restart_sec=$restart_sec_parsed
fi
burst_raw=$(directive StartLimitBurst "$unit" || true)
interval_raw=$(directive StartLimitIntervalSec "$unit" || true)
# Manager defaults (DefaultStartLimitBurst=5, DefaultStartLimitIntervalSec=10s)
# apply when the unit sets neither. Recovery must not depend on them:
# the policy test requires the unit to set both.
burst=${burst_raw:-5}
interval=${interval_raw:-10}
if [[ ! "$burst" =~ ^[0-9]+$ || ! "$interval" =~ ^[0-9]+$ ]]; then
  die "start limit is not a whole number (burst=$burst interval=$interval)"
fi
partof=$(directive PartOf "$unit" || true)
svc_type=$(directive Type "$unit" || true)
remain=$(directive RemainAfterExit "$unit" || true)
execstart=$(directive ExecStart "$unit" || true)
exec_condition=$(directive ExecCondition "$autodeploy" || true)

log() { printf '%s\n' "$*" | tee -a "$journal" >&2; }

# Copied from systemd 259 ratelimit_below, in whole seconds.
rate_limit_allows() {
  if [[ "$begin" -lt 0 || $((now - begin)) -gt "$interval" ]]; then
    begin=$now
    num=1
    return 0
  fi
  num=$((num + 1))
  [[ "$num" -le "$burst" ]]
}

do_start() {
  local why=$1
  if [[ "$why" == manual ]]; then
    manual_starts=$((manual_starts + 1))
  fi
  if ! rate_limit_allows; then
    # systemd 259 leaves Result=exit-code and logs this line. It does not
    # keep scheduling after the refusal, including once the window expires.
    state=failed
    result=exit-code
    scheduled=-1
    log "mealquest.service: Start request repeated too quickly."
    log "mealquest.service: Failed with result 'exit-code'."
    log "STEP start refused (Start request repeated too quickly) reason=$why t=${now}s"
    return 0
  fi
  starts=$((starts + 1))
  log "STEP ExecStart (#${starts}) reason=$why t=${now}s"
  if [[ "$fail_budget" -gt 0 ]]; then
    fail_budget=$((fail_budget - 1))
    state=failed
    result=exit-code
    log "mealquest-mealquest-1 exited (1): {\"level\":\"ERROR\",\"msg\":\"server stopped\",\"error\":\"auth: OIDC discovery: provider answered HTTP 503\"}"
    log "mealquest.service: Main process exited, code=exited, status=1/FAILURE"
    log "mealquest.service: Failed with result 'exit-code'."
    log "STEP unit failed (result=exit-code) t=${now}s"
    if [[ "$restart_mode" == on-failure ]]; then
      scheduled=$((now + restart_sec))
      log "mealquest.service: Scheduled restart job, restart counter is at ${starts}."
      log "STEP Restart=on-failure scheduled at t=${scheduled}s without a manual systemctl start"
    else
      scheduled=-1
      log "STEP Restart=$restart_mode schedules nothing"
    fi
  else
    state=active
    result=success
    scheduled=-1
    log "STEP ExecStart exited 0; RemainAfterExit=yes so the unit is active t=${now}s"
    log "Started MealQuest compose stack."
  fi
}

drain() {
  local guard=0
  local limit=$((burst + 3))
  while [[ "$scheduled" -ge 0 ]]; do
    now=$scheduled
    scheduled=-1
    do_start "restart-on-failure"
    guard=$((guard + 1))
    if [[ "$guard" -gt "$limit" ]]; then
      die "UNBOUNDED retries: scheduled more than StartLimitBurst ($burst) restarts"
    fi
  done
}

# The deployed condition, interpreted. Any other text is a different
# policy and this test will not invent a meaning for it.
auto_deploy_tick() {
  [[ "$exec_condition" == "/usr/bin/systemctl is-active --quiet mealquest.service" ]] \
    || die "ExecCondition is '$exec_condition', not systemctl is-active --quiet mealquest.service"
  if [[ "$state" == active ]]; then
    ticks_ran=$((ticks_ran + 1))
    log "STEP auto-deploy tick: ExecCondition true (unit active), not skipped"
  else
    ticks_skipped=$((ticks_skipped + 1))
    log "STEP auto-deploy tick: Skipped due to exec-condition (systemctl is-active --quiet mealquest.service is false, state=$state)"
  fi
}

docker_restart() {
  [[ "$partof" == "docker.service" ]] \
    || die "PartOf=$partof does not propagate a docker.service restart"
  log "STEP docker.service restart propagates to mealquest.service via PartOf=docker.service"
  # The stop half is a systemd operation, so Restart= does not apply to it.
  scheduled=-1
  if [[ "$state" != inactive ]]; then
    log "STEP ExecStop: docker compose down"
    state=inactive
  fi
  do_start "partof-docker-restart"
}

operator_stop() {
  log "STEP systemctl stop mealquest.service"
  scheduled=-1
  state=inactive
  result=success
  log "STEP unit inactive after a clean stop; Restart= does not apply"
}

reset() {
  : > "$journal"
  state=inactive
  result=
  begin=-1
  num=0
  now=0
  starts=0
  manual_starts=0
  scheduled=-1
  fail_budget=0
  ticks_ran=0
  ticks_skipped=0
}

journal_has() { grep -qF "$1" "$journal"; }

scenario_policy_is_bounded() {
  [[ "$svc_type" == oneshot ]] || die "Type=$svc_type; this recovery is for the oneshot start unit"
  [[ "$remain" == yes ]] || die "RemainAfterExit=$remain"
  [[ "$execstart" == "/usr/bin/docker compose -f /home/overseer/mealquest/deploy/compose.yaml up -d" ]] \
    || die "ExecStart is not compose up -d: $execstart"
  [[ "$restart_mode" == on-failure ]] || die "Restart=$restart_mode, want on-failure (a failed start is not retried)"
  [[ -n "$restart_sec_raw" ]] || die "RestartSec is not set on mealquest.service"
  [[ -n "$burst_raw" ]] || die "StartLimitBurst is not set on mealquest.service"
  [[ -n "$interval_raw" ]] || die "StartLimitIntervalSec is not set on mealquest.service"
  [[ "$restart_sec" -ge 10 && "$restart_sec" -le 60 ]] \
    || die "RestartSec=${restart_sec}s spins (under 10s) or waits too long (over 60s)"
  [[ "$burst" -ge 8 && "$burst" -le 30 ]] \
    || die "StartLimitBurst=$burst is outside 8..30"
  local span=$(( (burst - 1) * restart_sec ))
  [[ "$span" -ge 180 && "$span" -le 600 ]] \
    || die "retry span ${span}s is outside 180..600 (brief outage coverage, then stop)"
  # (now - begin) > interval opens a new window and would retry forever if
  # the refusing attempt lands outside it. +5s per start is ExecStart time.
  local margin=$(( burst * (restart_sec + 5) ))
  [[ "$interval" -ge "$margin" ]] \
    || die "StartLimitIntervalSec=${interval}s < ${margin}s (burst*(RestartSec+5)); retries would not stay bounded"
  echo "RESULT Restart=on-failure RestartSec=${restart_sec}s StartLimitBurst=$burst StartLimitIntervalSec=${interval}s span=${span}s"
}

scenario_exec_condition_is_active_only() {
  [[ "$exec_condition" == "/usr/bin/systemctl is-active --quiet mealquest.service" ]] \
    || die "ExecCondition is '$exec_condition'"
  echo "RESULT auto-deploy ExecCondition still requires mealquest.service to be active"
}

scenario_compose_is_not_a_second_start_path() {
  local restarts bad
  restarts=$(grep -E '^[[:space:]]*restart:' "$compose" || true)
  [[ -n "$restarts" ]] || die "compose.yaml sets no restart policy"
  bad=$(grep -E '^[[:space:]]*restart:' "$compose" | grep -v 'restart: "no"' || true)
  [[ -z "$bad" ]] || die "compose would start containers on its own: $bad"
  echo "RESULT compose restart stays \"no\"; mealquest.service remains the only start path"
}

scenario_no_other_unit_starts_the_stack() {
  local hit
  hit=$(grep -nE 'systemctl (start|restart) mealquest' "$here"/systemd/* || true)
  [[ -z "$hit" ]] || die "another unit starts the stack: $hit"
  echo "RESULT no systemd unit other than mealquest.service starts the stack"
}

# Production change that fails this: Restart= left at no, or the start
# limit too small to cover three failed starts. Literal want: 4 ExecStart
# runs, zero manual starts, one skipped tick, then a tick that runs.
scenario_oidc_race_recovers() {
  reset
  state=active
  fail_budget=3
  docker_restart
  [[ "$state" == failed && "$result" == exit-code ]] \
    || die "after the first 503 the unit should be failed/exit-code, state=$state result=$result"
  auto_deploy_tick
  [[ "$ticks_skipped" -eq 1 && "$ticks_ran" -eq 0 ]] \
    || die "ExecCondition should be false while the unit is failed (skipped=$ticks_skipped ran=$ticks_ran)"
  drain
  [[ "$manual_starts" -eq 0 ]] || die "recovery used a manual systemctl start"
  [[ "$state" == active && "$result" == success ]] \
    || die "stack did not recover (state=$state result=$result starts=$starts)"
  [[ "$starts" -eq 4 ]] || die "want 4 ExecStart runs (3 failures + success), got $starts"
  journal_has 'provider answered HTTP 503' || die "journal lacks the OIDC 503"
  journal_has 'Skipped due to exec-condition' || die "journal lacks ExecCondition false"
  journal_has 'Scheduled restart job' || die "journal lacks the scheduled restart"
  journal_has 'reason=restart-on-failure' || die "recovery was not Restart=on-failure"
  ! journal_has 'Start request repeated too quickly.' || die "a brief outage exhausted the start limit"
  auto_deploy_tick
  [[ "$ticks_ran" -eq 1 && "$ticks_skipped" -eq 1 ]] \
    || die "after recovery auto-deploy ran=$ticks_ran skipped=$ticks_skipped"
  echo "RESULT recovered without manual action; starts=4 manual=0; exec-condition skipped then ran"
}

# Production change that fails this: Restart=always, a watchdog that
# starts inactive units, or auto-deploy running while the unit is inactive.
scenario_operator_stop_stays_stopped() {
  reset
  state=active
  starts=0
  operator_stop
  local before=$starts
  now=$((now + 3600))
  drain
  auto_deploy_tick
  auto_deploy_tick
  auto_deploy_tick
  auto_deploy_tick
  [[ "$starts" -eq "$before" ]] || die "stopped stack was started (starts=$starts)"
  [[ "$manual_starts" -eq 0 ]] || die "manual start counted"
  [[ "$state" == inactive ]] || die "state=$state, want inactive"
  [[ "$ticks_ran" -eq 0 && "$ticks_skipped" -eq 4 ]] \
    || die "auto-deploy ran=$ticks_ran skipped=$ticks_skipped while stopped"
  ! journal_has 'Scheduled restart job' || die "a clean stop scheduled a restart"
  echo "RESULT systemctl stop stayed inactive for 3600s; four auto-deploy ticks skipped; starts=0"
}

# Production change that fails this: operator stop leaves the pending
# Restart= job queued.
scenario_stop_cancels_pending_restart() {
  reset
  state=active
  fail_budget=5
  docker_restart
  [[ "$starts" -eq 1 && "$state" == failed ]] || die "setup start did not fail (starts=$starts state=$state)"
  operator_stop
  now=$((now + restart_sec * burst + interval))
  drain
  [[ "$starts" -eq 1 ]] || die "operator stop was followed by further starts (starts=$starts)"
  [[ "$state" == inactive ]] || die "state=$state, want inactive"
  [[ "$manual_starts" -eq 0 ]] || die "manual start counted"
  echo "RESULT operator stop cancelled a pending failure restart; starts stayed 1"
}

# Production change that fails this: no start limit, or a window that
# resets and keeps scheduling. Literal: ExecStart runs burst times, then
# the next start is refused, and time passing does not add another.
scenario_start_limit_stops_the_loop() {
  reset
  state=active
  fail_budget=100000
  docker_restart
  drain
  [[ "$starts" -eq "$burst" ]] || die "ExecStart runs=$starts, want StartLimitBurst=$burst"
  [[ "$state" == failed && "$result" == exit-code ]] \
    || die "state=$state result=$result, want failed/exit-code"
  journal_has "Start request repeated too quickly." || die "journal lacks the rate-limit refusal"
  journal_has "Failed with result 'exit-code'." || die "journal lacks the failed result"
  local frozen=$starts
  now=$((now + interval + restart_sec + 5))
  drain
  [[ "$starts" -eq "$frozen" ]] || die "retried after the start limit (starts=$starts)"
  auto_deploy_tick
  [[ "$ticks_ran" -eq 0 && "$ticks_skipped" -eq 1 ]] \
    || die "auto-deploy ran against a unit past the start limit"
  [[ "$manual_starts" -eq 0 ]] || die "manual start counted"
  echo "RESULT stayed failed after $burst starts (Start request repeated too quickly); no further start after the window; auto-deploy skipped"
}

scenario_clean_exit_does_not_restart() {
  reset
  fail_budget=0
  do_start "initial"
  [[ "$state" == active && "$starts" -eq 1 ]] || die "clean start did not become active"
  now=$((now + interval))
  drain
  [[ "$starts" -eq 1 ]] || die "a clean oneshot exit was restarted (starts=$starts)"
  echo "RESULT a successful oneshot exit stays active with one ExecStart"
}

failures=0
run() {
  local name=$1
  shift
  local out st=0
  out=$("$@" 2>&1) || st=$?
  if [[ "$st" -eq 0 ]]; then
    echo "PASS  $name"
    grep '^RESULT ' <<< "$out" | sed 's/^/      /' || true
  else
    failures=$((failures + 1))
    echo "FAIL  $name"
    sed 's/^/      | /' <<< "$out"
  fi
}

run "failed-start recovery is bounded Restart=on-failure" scenario_policy_is_bounded
run "auto-deploy ExecCondition still requires an active stack" scenario_exec_condition_is_active_only
run "compose restart stays no" scenario_compose_is_not_a_second_start_path
run "no other unit starts the stack" scenario_no_other_unit_starts_the_stack
run "docker restart with OIDC 503 recovers without a manual start" scenario_oidc_race_recovers
run "a stack stopped with systemctl stop is not auto-started" scenario_operator_stop_stays_stopped
run "an operator stop cancels a pending failure restart" scenario_stop_cancels_pending_restart
run "a dependency that stays down hits the start limit and stays failed" scenario_start_limit_stops_the_loop
run "a clean oneshot exit is not restarted" scenario_clean_exit_does_not_restart

[[ "$failures" -eq 0 ]] || { echo "$failures stack-start recovery test(s) failed" >&2; exit 1; }
echo "stack-start recovery: all tests passed"
