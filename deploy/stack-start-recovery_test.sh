#!/usr/bin/env bash
# Offline proof that deploy/systemd/mealquest.service recovers the
# 2026-10-31 sequence without a manual start, and that a stack stopped
# with systemctl stop stays stopped.
#
# Docker restart → Authentik not ready → mealquest-mealquest-1 exits 1
# (auth: OIDC discovery: provider answered HTTP 503) → mealquest.service
# failed → mealquest-auto-deploy ExecCondition false. Recovery is the
# stack unit's own Restart=. That ExecCondition is unchanged
# (systemctl is-active --quiet mealquest.service): auto-deploy never
# starts or redeploys an operator-stopped stack, and a stack that has
# exhausted the start limit stays failed for an operator.
#
# The state machine is systemd 259. Restart=on-failure schedules another
# start after a non-zero ExecStart or a start timeout, and does not run
# when the service is stopped with systemctl stop. ratelimit_below allows
# the burst-th start (num <= burst) and refuses the next. With
# StartLimitIntervalSec=infinity the window never reopens. A refused
# start does not schedule another restart. Becoming active does not clear
# the counter; ExecStartPost's reset-failed does, and only after ExecStart
# exits 0 and reset-failed itself exits 0. A non-zero reset-failed is not
# ignored (no "-" prefix): it fails the unit, leaves the counter consumed,
# and Restart=on-failure retries inside the same StartLimitBurst.
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
# the policy test requires the unit to set both. "infinity" is the
# systemd.time(7) span that never opens a new window.
burst=${burst_raw:-5}
interval=${interval_raw:-10}
if [[ ! "$burst" =~ ^[0-9]+$ ]]; then
  die "StartLimitBurst is not a whole number (burst=$burst)"
fi
if [[ "$interval" != infinity && ! "$interval" =~ ^[0-9]+$ ]]; then
  die "StartLimitIntervalSec is not a whole number or infinity (interval=$interval)"
fi
timeout_start_raw=$(directive TimeoutStartSec "$unit" || true)
timeout_stop_raw=$(directive TimeoutStopSec "$unit" || true)
# Type=oneshot with no TimeoutStartSec= is infinity (systemd 259
# service_add_extras). An empty TimeoutStopSec= is the manager default, 90s.
if [[ -z "$timeout_start_raw" || "$timeout_start_raw" == infinity ]]; then
  timeout_start=infinity
else
  timeout_start=$(parse_seconds "$timeout_start_raw")
  [[ "$timeout_start" != INVALID && -n "$timeout_start" ]] \
    || die "TimeoutStartSec=$timeout_start_raw is not a whole number of seconds or infinity"
fi
if [[ -z "$timeout_stop_raw" ]]; then
  timeout_stop=90
elif [[ "$timeout_stop_raw" == infinity ]]; then
  timeout_stop=infinity
else
  timeout_stop=$(parse_seconds "$timeout_stop_raw")
  [[ "$timeout_stop" != INVALID && -n "$timeout_stop" ]] \
    || die "TimeoutStopSec=$timeout_stop_raw is not a whole number of seconds or infinity"
fi
execstartpost=$(directive ExecStartPost "$unit" || true)
# A leading "-" is systemd's ignore-failure prefix. reset-failed still has
# to exit 0 to clear the counter; the prefix only hides a non-zero status.
reset_ignores_failure=0
reset_is_reset=0
case "$execstartpost" in
  "-/usr/bin/systemctl reset-failed mealquest.service")
    reset_ignores_failure=1
    reset_is_reset=1
    ;;
  "/usr/bin/systemctl reset-failed mealquest.service")
    reset_is_reset=1
    ;;
esac
partof=$(directive PartOf "$unit" || true)
svc_type=$(directive Type "$unit" || true)
remain=$(directive RemainAfterExit "$unit" || true)
execstart=$(directive ExecStart "$unit" || true)
exec_condition=$(directive ExecCondition "$autodeploy" || true)

log() { printf '%s\n' "$*" | tee -a "$journal" >&2; }

# Copied from systemd 259 ratelimit_below, in whole seconds.
# StartLimitIntervalSec=infinity: (now - begin) > interval is never true,
# so the counter is not time-reset. begin < 0 is the initial state and the
# state after ExecStartPost reset-failed (ratelimit_reset).
rate_limit_allows() {
  local expired=0
  if [[ "$interval" != infinity && "$begin" -ge 0 && $((now - begin)) -gt "$interval" ]]; then
    expired=1
  fi
  if [[ "$begin" -lt 0 || "$expired" -eq 1 ]]; then
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
    # systemd 259 checks the start limit in service_test_startable, before
    # service_start resets result. service_enter_dead keeps a prior failure
    # result, so a streak ends as exit-code or timeout and the journal
    # still records "Start request repeated too quickly." The refused start
    # does not run ExecStart and does not schedule another restart.
    state=failed
    if [[ "$result" != exit-code && "$result" != timeout ]]; then
      result=start-limit-hit
    fi
    scheduled=-1
    log "mealquest.service: Start request repeated too quickly."
    log "mealquest.service: Failed with result '${result}'."
    log "STEP start refused (Start request repeated too quickly) reason=$why t=${now}s"
    return 0
  fi
  starts=$((starts + 1))
  # The rate limit is checked at the beginning of the start. ExecStart then
  # consumes wall clock before the next check. A hang past TimeoutStartSec
  # is killed (systemd 259: "start operation timed out. Terminating."); if
  # the process ignores SIGTERM, TimeoutStopSec is the grace before SIGKILL.
  local ran=$attempt_secs
  local killed=0
  if [[ "$timeout_start" != infinity && "$ran" -gt "$timeout_start" ]]; then
    [[ "$timeout_stop" != infinity ]] || die "TimeoutStopSec=infinity; a hung ExecStart has no finite kill grace"
    ran=$((timeout_start + timeout_stop))
    killed=1
    log "mealquest.service: start operation timed out. Terminating."
    log "STEP ExecStart (#${starts}) reason=$why t=${now}s killed at TimeoutStartSec=${timeout_start}s"
  else
    log "STEP ExecStart (#${starts}) reason=$why t=${now}s"
  fi
  now=$((now + ran))
  if [[ "$fail_budget" -gt 0 ]]; then
    fail_budget=$((fail_budget - 1))
    state=failed
    if [[ "$killed" -eq 1 ]]; then
      result=timeout
      log "mealquest.service: Main process exited, code=killed, status=15/TERM"
      log "mealquest.service: Failed with result 'timeout'."
      log "STEP unit failed (result=timeout) t=${now}s"
    else
      result=exit-code
      log "mealquest-mealquest-1 exited (1): {\"level\":\"ERROR\",\"msg\":\"server stopped\",\"error\":\"auth: OIDC discovery: provider answered HTTP 503\"}"
      log "mealquest.service: Main process exited, code=exited, status=1/FAILURE"
      log "mealquest.service: Failed with result 'exit-code'."
      log "STEP unit failed (result=exit-code) t=${now}s"
    fi
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
    # systemd 259 does not clear start_ratelimit when the unit becomes
    # active (only unit_reset_failed does). ExecStartPost runs only after
    # ExecStart exits 0. A leading "-" ignores a non-zero reset-failed, so
    # the unit stays active and the counter stays consumed. Without it, a
    # non-zero reset-failed fails the unit. Restart=on-failure retries, and
    # because the counter was not cleared those retries stay inside
    # StartLimitBurst. systemd skips ExecStop= when this failure happens
    # before the service is fully up; the bound is the start limit, not a
    # stop.
    if [[ "$reset_is_reset" -eq 1 && "$reset_fails" -eq 0 ]]; then
      begin=-1
      num=0
      log "STEP ExecStartPost reset-failed; start-limit counter cleared"
    elif [[ "$reset_is_reset" -eq 1 && "$reset_fails" -eq 1 ]]; then
      log "STEP ExecStartPost reset-failed exited non-zero; start-limit counter not cleared"
      if [[ "$reset_ignores_failure" -eq 1 ]]; then
        log "STEP ExecStartPost failure ignored; unit stays active with a consumed start-limit counter"
      else
        state=failed
        result=exit-code
        log "mealquest.service: Control process exited, code=exited, status=1/FAILURE"
        log "mealquest.service: Failed with result 'exit-code'."
        log "STEP ExecStartPost failure failed the unit"
        if [[ "$restart_mode" == on-failure ]]; then
          scheduled=$((now + restart_sec))
          log "mealquest.service: Scheduled restart job, restart counter is at ${starts}."
          log "STEP Restart=on-failure scheduled at t=${scheduled}s after a failed reset-failed"
        else
          scheduled=-1
          log "STEP Restart=$restart_mode schedules nothing after a failed reset-failed"
        fi
      fi
    fi
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
  attempt_secs=0
  reset_fails=0
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
  # A finite window is not a total bound: a slow ExecStart can make
  # (now - begin) > interval before the burst is spent, and ratelimit_below
  # then starts a new window. infinity never does.
  [[ "$interval" == infinity ]] \
    || die "StartLimitIntervalSec=$interval is a window a slow ExecStart can outrun; want infinity"
  [[ "$timeout_start" != infinity ]] \
    || die "TimeoutStartSec is unset; Type=oneshot would wait forever on a hung ExecStart"
  [[ "$timeout_start" -ge 120 && "$timeout_start" -le 600 ]] \
    || die "TimeoutStartSec=${timeout_start}s is outside 120..600"
  [[ -n "$timeout_stop_raw" && "$timeout_stop" != infinity ]] \
    || die "TimeoutStopSec must be set and finite (got '${timeout_stop_raw}')"
  [[ "$timeout_stop" -ge 30 && "$timeout_stop" -le 120 ]] \
    || die "TimeoutStopSec=${timeout_stop}s is outside 30..120"
  [[ "$execstartpost" == "/usr/bin/systemctl reset-failed mealquest.service" ]] \
    || die "ExecStartPost must clear the start-limit counter after a successful ExecStart and must not ignore a failed reset (got '$execstartpost')"
  local quick=$(( burst * restart_sec ))
  local worst=$(( burst * (timeout_start + timeout_stop + restart_sec) ))
  echo "RESULT Restart=on-failure RestartSec=${restart_sec}s StartLimitBurst=$burst StartLimitIntervalSec=infinity TimeoutStartSec=${timeout_start}s TimeoutStopSec=${timeout_stop}s quick=${quick}s worst=${worst}s span=${span}s"
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
  now=$((now + restart_sec * burst + 100000))
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
  # Time passing must not grant another attempt. infinity has no window
  # to wait out; a large step covers a finite window too.
  now=$((now + 100000))
  drain
  [[ "$starts" -eq "$frozen" ]] || die "retried after the start limit (starts=$starts)"
  auto_deploy_tick
  [[ "$ticks_ran" -eq 0 && "$ticks_skipped" -eq 1 ]] \
    || die "auto-deploy ran against a unit past the start limit"
  [[ "$manual_starts" -eq 0 ]] || die "manual start counted"
  echo "RESULT stayed failed after $burst starts (Start request repeated too quickly); no further start; auto-deploy skipped"
}

# Production change that fails this: StartLimitIntervalSec is a finite
# window a slow ExecStart can outrun, so retries continue past
# StartLimitBurst. Each failed start takes 120s, longer than a
# one-second-per-attempt budget and longer than 900/20s. The clock
# advances during ExecStart, before the next rate-limit check.
scenario_slow_failure_is_a_total_bound() {
  reset
  state=active
  fail_budget=100000
  attempt_secs=120
  docker_restart
  drain
  [[ "$starts" -eq "$burst" ]] || die "slow ExecStart runs=$starts, want a total bound of StartLimitBurst=$burst (t=${now}s)"
  [[ "$state" == failed && "$result" == exit-code ]] \
    || die "state=$state result=$result, want failed/exit-code"
  journal_has "Start request repeated too quickly." || die "journal lacks the start-limit refusal"
  [[ "$interval" == infinity ]] || die "StartLimitIntervalSec=$interval is still a window a slower start can outrun"
  local frozen=$starts
  local stamp=$now
  [[ "$stamp" -gt $((burst * attempt_secs)) ]] \
    || die "slow attempts did not advance the clock (t=${stamp}s)"
  # Past any finite window the old rate limit would have reopened.
  now=$((now + 100000))
  drain
  [[ "$starts" -eq "$frozen" ]] || die "retried after the total bound (starts=$starts, was $frozen)"
  auto_deploy_tick
  [[ "$ticks_ran" -eq 0 && "$ticks_skipped" -eq 1 ]] \
    || die "auto-deploy ran against a stack that exhausted the retry budget (ran=$ticks_ran skipped=$ticks_skipped)"
  [[ "$manual_starts" -eq 0 ]] || die "manual start counted"
  echo "RESULT slow ExecStart (${attempt_secs}s) stopped at $starts starts; stayed failed at t=${stamp}s; auto-deploy skipped"
}

# Production change that fails this: a successful start leaves the
# infinity counter consumed, so the next failure streak is shorter than
# StartLimitBurst (or, with a finite window, slow attempts run forever).
scenario_success_restores_budget_for_the_next_streak() {
  reset
  state=active
  fail_budget=3
  attempt_secs=0
  docker_restart
  drain
  [[ "$state" == active && "$starts" -eq 4 ]] \
    || die "recovery setup failed (state=$state starts=$starts)"
  local used=$starts
  fail_budget=100000
  attempt_secs=120
  docker_restart
  drain
  local second=$((starts - used))
  [[ "$second" -eq "$burst" ]] \
    || die "after a successful start, slow failures ran $second, want a fresh StartLimitBurst=$burst"
  [[ "$state" == failed ]] || die "second streak did not stay failed (state=$state)"
  journal_has "Start request repeated too quickly." || die "journal lacks the start-limit refusal"
  echo "RESULT a successful start cleared the counter; the next slow streak stopped at $second"
}

# Production change that fails this: TimeoutStartSec unset (oneshot's
# default is infinity) or StartLimitIntervalSec a window the hang can
# outrun. The process ignores SIGTERM, so each attempt costs
# TimeoutStartSec + TimeoutStopSec + RestartSec, burst times, then stops.
scenario_hung_start_is_capped_and_bounded() {
  reset
  state=active
  fail_budget=100000
  attempt_secs=100000
  docker_restart
  drain
  [[ "$starts" -eq "$burst" ]] || die "hung ExecStart runs=$starts, want StartLimitBurst=$burst (t=${now}s)"
  [[ "$state" == failed && "$result" == timeout ]] \
    || die "state=$state result=$result, want failed/timeout"
  journal_has "start operation timed out. Terminating." || die "journal lacks the start timeout"
  journal_has "Start request repeated too quickly." || die "journal lacks the start-limit refusal"
  [[ "$interval" == infinity ]] || die "StartLimitIntervalSec=$interval can be outrun by a longer hang"
  [[ "$timeout_start" != infinity && "$timeout_stop" != infinity ]] \
    || die "TimeoutStartSec=$timeout_start TimeoutStopSec=$timeout_stop is not a finite cap"
  local worst=$(( burst * (timeout_start + timeout_stop + restart_sec) ))
  [[ "$now" -eq "$worst" ]] || die "hung streak wall clock ${now}s, want ${worst}s"
  local frozen=$starts
  now=$((now + 100000))
  drain
  [[ "$starts" -eq "$frozen" ]] || die "retried after the hung-start bound (starts=$starts)"
  auto_deploy_tick
  [[ "$ticks_ran" -eq 0 && "$ticks_skipped" -eq 1 ]] \
    || die "auto-deploy ran against a stack that exhausted the retry budget"
  echo "RESULT hung ExecStart stopped at $starts after ${worst}s; stayed failed; auto-deploy skipped"
}

# Production change that fails this: a leading "-" on ExecStartPost, so
# systemd ignores a non-zero reset-failed. The unit is then active with
# the start-limit counter still consumed — a successful start that did
# not restore the budget. Without the prefix the same failure fails the
# unit, and Restart=on-failure retries stay inside StartLimitBurst
# because the counter was not cleared and the window never reopens.
scenario_failed_reset_is_not_a_successful_fresh_budget_start() {
  if [[ "$execstartpost" == "-/usr/bin/systemctl reset-failed mealquest.service" ]]; then
    reset
    reset_fails=1
    fail_budget=0
    do_start "initial"
    [[ "$state" == active && "$begin" -ge 0 && "$num" -ge 1 ]] \
      || die "harness did not model an ignored reset failure (state=$state begin=$begin num=$num)"
    die "a failed reset-failed was reported as a successful start with a consumed counter (state=$state begin=$begin num=$num)"
  fi
  [[ "$execstartpost" == "/usr/bin/systemctl reset-failed mealquest.service" ]] \
    || die "ExecStartPost must fail the unit when reset-failed fails (got '$execstartpost')"

  reset
  reset_fails=1
  fail_budget=0
  do_start "initial"
  [[ "$state" == failed && "$result" == exit-code ]] \
    || die "failed reset-failed reported a successful start (state=$state result=$result)"
  [[ "$begin" -ge 0 && "$num" -ge 1 ]] \
    || die "failed reset-failed cleared the start-limit counter (begin=$begin num=$num)"
  ! journal_has "start-limit counter cleared" \
    || die "a failed reset was logged as a fresh budget"
  journal_has "ExecStartPost failure failed the unit" \
    || die "journal lacks the failed reset-failed"
  drain
  [[ "$starts" -eq "$burst" ]] \
    || die "failed reset-failed ran $starts starts, want StartLimitBurst=$burst"
  [[ "$state" == failed ]] || die "reset-failure streak left state=$state"
  journal_has "Start request repeated too quickly." \
    || die "a failed reset-failed retried without hitting the start limit"
  [[ "$manual_starts" -eq 0 ]] || die "manual start counted"
  local frozen=$starts
  now=$((now + 100000))
  drain
  [[ "$starts" -eq "$frozen" ]] \
    || die "reset-failure streak continued after the total bound (starts=$starts)"
  auto_deploy_tick
  [[ "$ticks_ran" -eq 0 && "$ticks_skipped" -eq 1 ]] \
    || die "auto-deploy ran against a unit failed by reset-failed (ran=$ticks_ran skipped=$ticks_skipped)"
  echo "RESULT a failed reset-failed failed the start; streak stopped at $starts (StartLimitBurst) and did not clear the counter"
}

scenario_clean_exit_does_not_restart() {
  reset
  fail_budget=0
  do_start "initial"
  [[ "$state" == active && "$starts" -eq 1 ]] || die "clean start did not become active"
  now=$((now + 100000))
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
run "a slow failed start still stops at the total bound" scenario_slow_failure_is_a_total_bound
run "a hung start is capped and still stops at the total bound" scenario_hung_start_is_capped_and_bounded
run "a successful start restores a full budget for the next streak" scenario_success_restores_budget_for_the_next_streak
run "a failed reset-failed is not a successful fresh-budget start" scenario_failed_reset_is_not_a_successful_fresh_budget_start
run "a clean oneshot exit is not restarted" scenario_clean_exit_does_not_restart

[[ "$failures" -eq 0 ]] || { echo "$failures stack-start recovery test(s) failed" >&2; exit 1; }
echo "stack-start recovery: all tests passed"
