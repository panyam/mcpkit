#!/usr/bin/env bash
# Run the full mcpkit test suite as labelled stages with per-stage logs,
# then render the HTML report via scripts/test-report.sh.
#
# Runner-agnostic: the root Makefile + justfile `testall` recipes both call
# this with no arguments. Each stage is dispatched by running its leaf script
# directly (see resolve_script) — there is no make/just invocation anywhere in
# this file. The logical stage tokens (e.g. testconf-skills) are retained as
# the run_stage target argument so scripts/check_local_suites.py can validate
# the suite→stage wiring; resolve_script maps each token to its script path.
#
# Env:
#   REPORT_DIR  output directory (default: tests/reports)
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPORT_DIR="${REPORT_DIR:-tests/reports}"
cd "$ROOT"

# resolve_script maps a logical stage token to the leaf script that runs it.
# Every stage in the suite runs a script directly — no runner indirection.
resolve_script() {
    case "$1" in
        cover-html)                            echo "scripts/cover-html.sh" ;;
        test-race)                             echo "scripts/test-race.sh" ;;
        test-auth)                             echo "scripts/test-auth.sh" ;;
        test-ui)                               echo "ext/ui/scripts/test.sh" ;;
        test-protogen)                         echo "scripts/test-protogen.sh" ;;
        test-otel)                             echo "scripts/test-otel.sh" ;;
        test-otel-example)                     echo "scripts/test-otel-example.sh" ;;
        test-e2e)                              echo "scripts/test-e2e.sh" ;;
        test-experimental-events)              echo "experimental/scripts/test-events.sh" ;;
        test-experimental-events-clients-go)   echo "experimental/scripts/test-events-clients-go.sh" ;;
        test-experimental-events-stores-gorm)  echo "experimental/scripts/test-events-stores-gorm.sh" ;;
        test-experimental-events-stores-redis) echo "experimental/scripts/test-events-stores-redis.sh" ;;
        test-experimental-events-discord)      echo "experimental/scripts/test-events-discord.sh" ;;
        test-experimental-events-telegram)     echo "experimental/scripts/test-events-telegram.sh" ;;
        testconf)                              echo "scripts/conformance-test.sh" ;;
        testconfauth)                          echo "scripts/conformance-auth-test.sh" ;;
        testconf-tasks)                        echo "conformance/scripts/conf-tasks.sh" ;;
        testconf-tasks-v2)                     echo "conformance/scripts/conf-tasks-v2.sh" ;;
        testconf-mrtr)                         echo "conformance/scripts/conf-mrtr.sh" ;;
        testconf-file-inputs)                  echo "conformance/scripts/conf-file-inputs.sh" ;;
        testconf-auth-server)                  echo "conformance/scripts/conf-auth-server.sh" ;;
        testconf-skills)                       echo "conformance/scripts/conf-skills.sh" ;;
        testkcl-auto)                          echo "scripts/testkcl-auto.sh" ;;
        *) echo "resolve_script: unknown stage token '$1'" >&2; return 1 ;;
    esac
}

mkdir -p "$REPORT_DIR"
rm -f "$REPORT_DIR"/stage-*.log
echo "=== MCPKit Comprehensive Test Suite ===" | tee "$REPORT_DIR/run.log"
echo "Started: $(date)" | tee -a "$REPORT_DIR/run.log"
PASS=0; FAIL=0; INFO=0; STAGES=""

# run_stage runs a stage script with a per-stage log file.
# Each stage writes to $REPORT_DIR/stage-<label>.log (not the shared run.log).
# Usage: run_stage STEP_NUM TOTAL LABEL TOKEN [info]
#
# TOKEN is the logical stage name (resolved to a script via resolve_script).
# A 5th argument of "info" is the soft-failure mode for experimental or
# in-flight conformance work where the suite SHOULD surface in testall
# reports but MUST NOT block the build. Failure is recorded as INFO and
# counted separately so a failing experimental stage does not show up in
# the PASS/FAIL tallies.
run_stage() {
    local STEP=$1 TOTAL=$2 LABEL=$3 TOKEN=$4 MODE=${5:-}
    local SCRIPT; SCRIPT=$(resolve_script "$TOKEN") || exit 1
    local STAGE_LOG=$REPORT_DIR/stage-$LABEL.log
    local STAGE_START=$(date +%s)
    if [ "$MODE" = "info" ]; then
        echo "--- [$STEP/$TOTAL] $LABEL (informational) ---" | tee -a "$REPORT_DIR/run.log"
        echo "=== Stage $STEP/$TOTAL: $LABEL (bash $SCRIPT) [informational] ===" > "$STAGE_LOG"
    else
        echo "--- [$STEP/$TOTAL] $LABEL ---" | tee -a "$REPORT_DIR/run.log"
        echo "=== Stage $STEP/$TOTAL: $LABEL (bash $SCRIPT) ===" > "$STAGE_LOG"
    fi
    echo "Started: $(date)" >> "$STAGE_LOG"
    if bash "$ROOT/$SCRIPT" >> "$STAGE_LOG" 2>&1; then
        local ELAPSED=$(($(date +%s) - STAGE_START))
        if [ "$MODE" = "info" ]; then
            echo "  PASS: $LABEL (${ELAPSED}s, informational)" | tee -a "$REPORT_DIR/run.log"
        else
            echo "  PASS: $LABEL (${ELAPSED}s)" | tee -a "$REPORT_DIR/run.log"
        fi
        PASS=$((PASS+1)); STAGES="$STAGES $LABEL:PASS:$SCRIPT:${ELAPSED}s"
    else
        local ELAPSED=$(($(date +%s) - STAGE_START))
        if [ "$MODE" = "info" ]; then
            echo "  INFO: $LABEL (${ELAPSED}s, informational, not counted as failure)" | tee -a "$REPORT_DIR/run.log"
            INFO=$((INFO+1)); STAGES="$STAGES $LABEL:INFO:$SCRIPT:${ELAPSED}s"
        else
            echo "  FAIL: $LABEL (${ELAPSED}s)" | tee -a "$REPORT_DIR/run.log"
            FAIL=$((FAIL+1)); STAGES="$STAGES $LABEL:FAIL:$SCRIPT:${ELAPSED}s"
        fi
        echo "  --- $LABEL tail ---"; tail -20 "$STAGE_LOG"; echo "  ---"
    fi
    echo "Finished: $(date) (elapsed ${ELAPSED}s)" >> "$STAGE_LOG"
}

echo "" | tee -a "$REPORT_DIR/run.log"
run_stage 1 9 unit+coverage cover-html
run_stage 2 9 race test-race
run_stage 3 9 auth test-auth
run_stage 4 9 ui test-ui
run_stage 5 9 protogen test-protogen
run_stage 5a 9 otel-adapter test-otel
run_stage 5b 9 otel-example test-otel-example
run_stage 6 9 e2e test-e2e
run_stage 7a 9 experimental-events test-experimental-events
run_stage 7b 9 experimental-events-clients-go test-experimental-events-clients-go
run_stage 7c 9 experimental-events-stores-gorm test-experimental-events-stores-gorm
run_stage 7d 9 experimental-events-stores-redis test-experimental-events-stores-redis
run_stage 7e 9 experimental-events-discord test-experimental-events-discord
run_stage 7f 9 experimental-events-telegram test-experimental-events-telegram
run_stage 8a 9 conformance testconf
run_stage 8b 9 auth-conformance testconfauth
run_stage 8c 9 tasks-conformance testconf-tasks
run_stage 8d 9 tasks-v2-conformance testconf-tasks-v2
run_stage 8e 9 mrtr-conformance testconf-mrtr
run_stage 8f 9 file-inputs-conformance testconf-file-inputs
run_stage 8g 9 auth-server-conformance testconf-auth-server
run_stage 8h 9 skills-conformance testconf-skills info
run_stage 9 9 keycloak testkcl-auto
echo "" | tee -a "$REPORT_DIR/run.log"
echo "=== Results: $PASS passed, $FAIL failed, $INFO informational ===" | tee -a "$REPORT_DIR/run.log"
echo "Finished: $(date)" | tee -a "$REPORT_DIR/run.log"
echo "Per-stage logs: $REPORT_DIR/stage-*.log"
REPORT_DIR="$REPORT_DIR" bash "$ROOT/scripts/test-report.sh" "$STAGES"
echo "HTML report: $REPORT_DIR/report.html"
[ $FAIL -eq 0 ]
