#!/bin/bash
# Run all README exercises against a running tasks server.
# Prints each command and its result for easy verification.
#
# Usage:
#   go run . -addr :8080 &        # start server first
#   bash run-exercises.sh          # default port 8080
#   bash run-exercises.sh 8090     # custom port

set -e

PORT="${1:-8080}"
BASE="http://localhost:$PORT/mcp"
CT="Content-Type: application/json"
ACCEPT="Accept: application/json, text/event-stream"

# Colors
GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
DIM='\033[2m'
NC='\033[0m'

# Helper: send request, parse SSE or JSON, print result
mcp() {
  local raw
  raw=$(curl -s "$@")
  local json
  json=$(echo "$raw" | grep '^data: ' | tail -1 | sed 's/^data: //')
  if [ -z "$json" ]; then json="$raw"; fi
  echo "$json" | tee /tmp/mcp-body.json | jq -S . 2>/dev/null || echo "$json"
}

exercise() {
  echo ""
  echo -e "${GREEN}═══════════════════════════════════════${NC}"
  echo -e "${GREEN}  Exercise $1: $2${NC}"
  echo -e "${GREEN}═══════════════════════════════════════${NC}"
  echo ""
}

cmd() {
  echo -e "${DIM}$ $1${NC}"
}

expect() {
  echo -e "${YELLOW}Expected: $1${NC}"
  echo ""
}

# ============================================================================
echo -e "${CYAN}Initializing session against $BASE ...${NC}"
# ============================================================================

RAW=$(curl -s -D /tmp/mcp-headers.txt "$BASE" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{"elicitation":{},"sampling":{}},"clientInfo":{"name":"exercise-runner","version":"1.0"}}}')
JSON=$(echo "$RAW" | grep '^data: ' | tail -1 | sed 's/^data: //')
if [ -z "$JSON" ]; then JSON="$RAW"; fi
SESSION_ID=$(grep -i mcp-session-id /tmp/mcp-headers.txt | awk '{print $2}' | tr -d '\r\n')

if [ -z "$SESSION_ID" ]; then
  echo "ERROR: Failed to get session ID. Is the server running on port $PORT?"
  exit 1
fi

echo "Session: $SESSION_ID"
echo "$JSON" | jq -S . 2>/dev/null || echo "$JSON"

# Send initialized notification
curl -s "$BASE" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $SESSION_ID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' > /dev/null 2>&1

SH="Mcp-Session-Id: $SESSION_ID"

# ============================================================================
exercise 1 "Sync tool call (greet)"
# ============================================================================
cmd 'tools/call greet {name: "World"}'
expect 'Hello, World!'
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"greet","arguments":{"name":"World"}}}'

# ============================================================================
exercise 2 "Async computation (slow_compute with task hint)"
# ============================================================================
cmd 'tools/call slow_compute {seconds: 3, label: "pi"} + task hint'
expect 'CreateTaskResult with taskId, status: working'
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":3,"label":"pi"},"task":{}}}'
TASK_ID=$(python3 -c "import json; print(json.load(open('/tmp/mcp-body.json')).get('result',{}).get('task',{}).get('taskId',''))" 2>/dev/null)
echo "TASK_ID=$TASK_ID"

# ============================================================================
exercise 3 "Poll task status (tasks/get)"
# ============================================================================
cmd "tasks/get $TASK_ID"
expect 'status: working (if polled quickly) or completed'
sleep 1
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$TASK_ID\"}}"

# ============================================================================
exercise 4 "Failing job (required task support)"
# ============================================================================
cmd 'tools/call failing_job + task hint'
expect 'CreateTaskResult, then status → failed after 1s'
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"failing_job","arguments":{},"task":{}}}'
FAIL_ID=$(python3 -c "import json; print(json.load(open('/tmp/mcp-body.json')).get('result',{}).get('task',{}).get('taskId',''))" 2>/dev/null)
sleep 2
cmd "tasks/get $FAIL_ID (after 2s)"
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":6,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$FAIL_ID\"}}"

# ============================================================================
exercise 5 "Elicitation from task (confirm_delete) — partial"
# ============================================================================
cmd 'tools/call confirm_delete {filename: "important.txt"} + task hint'
expect 'CreateTaskResult (status: working or input_required depending on timing), then tasks/get → input_required'
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"confirm_delete","arguments":{"filename":"important.txt"},"task":{}}}'
ELICIT_ID=$(python3 -c "import json; print(json.load(open('/tmp/mcp-body.json')).get('result',{}).get('task',{}).get('taskId',''))" 2>/dev/null)
sleep 1
cmd "tasks/get $ELICIT_ID (should show input_required)"
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":8,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$ELICIT_ID\"}}"

# ============================================================================
exercise 6 "Sampling from task (write_haiku) — partial"
# ============================================================================
cmd 'tools/call write_haiku {topic: "ocean"} + task hint'
expect 'CreateTaskResult (status: working or input_required depending on timing), then tasks/get → input_required'
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"write_haiku","arguments":{"topic":"ocean"},"task":{}}}'
SAMPLE_ID=$(python3 -c "import json; print(json.load(open('/tmp/mcp-body.json')).get('result',{}).get('task',{}).get('taskId',''))" 2>/dev/null)
sleep 1
cmd "tasks/get $SAMPLE_ID (should show input_required)"
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":10,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$SAMPLE_ID\"}}"

# ============================================================================
exercise 7 "Cancel a running task"
# ============================================================================
cmd 'tools/call slow_compute {seconds: 60} + task hint, then cancel'
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":60,"label":"cancel-me"},"task":{}}}'
CANCEL_ID=$(python3 -c "import json; print(json.load(open('/tmp/mcp-body.json')).get('result',{}).get('task',{}).get('taskId',''))" 2>/dev/null)
cmd "tasks/cancel $CANCEL_ID"
expect 'status: cancelled'
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":12,\"method\":\"tasks/cancel\",\"params\":{\"taskId\":\"$CANCEL_ID\"}}"

# ============================================================================
exercise 8 "List all tasks"
# ============================================================================
cmd 'tasks/list'
expect 'Array of all tasks created in this session'
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":13,"method":"tasks/list","params":{}}'

# ============================================================================
exercise 9 "TTL expiry (Phase 2)"
# ============================================================================
cmd 'tools/call slow_compute {seconds: 1} + task hint with ttl: 3000'
expect 'Task created, completes after 1s, cleaned up after 3s TTL'
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":1,"label":"ttl-test"},"task":{"ttl":3000}}}'
TTL_ID=$(python3 -c "import json; print(json.load(open('/tmp/mcp-body.json')).get('result',{}).get('task',{}).get('taskId',''))" 2>/dev/null)
sleep 2
cmd "tasks/get $TTL_ID (after 2s — should still exist)"
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":15,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$TTL_ID\"}}"
echo ""
echo -e "${YELLOW}Waiting 3s for TTL to expire...${NC}"
sleep 3
cmd "tasks/get $TTL_ID (after 5s total — should be gone)"
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":16,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$TTL_ID\"}}"

# ============================================================================
exercise "3+4" "tasks/result (wait for completed task)"
# ============================================================================
sleep 1  # let slow_compute from exercise 2 finish
cmd "tasks/result $TASK_ID (should return completed result)"
expect 'ToolResult with content + _meta[related-task]'
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":17,\"method\":\"tasks/result\",\"params\":{\"taskId\":\"$TASK_ID\"}}"

# ============================================================================
exercise 10 "Session isolation (Phase 3)"
# ============================================================================
cmd 'Initialize a second session, try to access first session task'

# Initialize session B
RAW_B=$(curl -s -D /tmp/mcp-headers-b.txt "$BASE" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"session-b","version":"1.0"}}}')
SESSION_B=$(grep -i mcp-session-id /tmp/mcp-headers-b.txt | awk '{print $2}' | tr -d '\r\n')
curl -s "$BASE" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $SESSION_B" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' > /dev/null 2>&1
echo "Session B: $SESSION_B"

# Create a task in session A (reuse $SESSION_ID)
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":20,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":10,"label":"session-test"},"task":{}}}'
ISO_TASK_ID=$(jq -r '.result.task.taskId' /tmp/mcp-body.json 2>/dev/null)
echo "Task created in session A: $ISO_TASK_ID"

cmd "Session B tries tasks/get on session A's task"
expect 'Error: task not found (cross-session access denied)'
mcp "$BASE" -H "Mcp-Session-Id: $SESSION_B" -H "$CT" -H "$ACCEPT" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":21,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$ISO_TASK_ID\"}}"

cmd "Session A can still access its own task"
expect 'Task info with status: working'
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":22,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$ISO_TASK_ID\"}}"

cmd "Session B's tasks/list should NOT include session A's tasks"
expect 'Empty or only session B tasks'
mcp "$BASE" -H "Mcp-Session-Id: $SESSION_B" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":23,"method":"tasks/list","params":{}}'

# ============================================================================
exercise 11 "Store API: double-complete rejected (Phase 4) ✅"
# ============================================================================
cmd 'Create task, wait for completion, try tasks/result twice'
expect 'Both calls return same result (no error on second call — but store should guard internally)'
echo -e "${YELLOW}Note: Phase 4 is internal store safety — not directly observable via protocol.${NC}"
echo -e "${YELLOW}The test suite (TestStoreAtomicResult) covers this.${NC}"

# ============================================================================
exercise 12 "Cancel propagation (Phase 5) ✅"
# ============================================================================
cmd 'Start 60s computation, cancel, check if goroutine actually stops'
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":30,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":60,"label":"cancel-propagation"},"task":{}}}'
PROP_ID=$(jq -r '.result.task.taskId' /tmp/mcp-body.json 2>/dev/null)
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":31,\"method\":\"tasks/cancel\",\"params\":{\"taskId\":\"$PROP_ID\"}}"
expect 'Status: cancelled (immediate). Server log should NOT show "finished cancel-propagation" after 60s.'
echo -e "${YELLOW}Today: status shows cancelled, but goroutine keeps sleeping for 60s.${NC}"
echo -e "${YELLOW}After Phase 5: goroutine receives context cancellation and exits immediately.${NC}"

# ============================================================================
exercise 13 "Status notifications (Phase 6) ✅"
# ============================================================================
cmd 'Open GET SSE stream, create task, watch for notifications/tasks/status'
expect 'SSE stream receives working → completed notifications'
echo -e "${YELLOW}Testing SSE notifications requires a background listener...${NC}"

# Start SSE listener in background
curl -s -N "$BASE" -H "$ACCEPT" -H "Mcp-Session-Id: $SESSION_ID" > /tmp/sse-notifications.txt 2>&1 &
SSE_PID=$!
sleep 0.5

# Create a fast task to trigger notifications
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":32,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":1,"label":"notify-test"},"task":{}}}'
sleep 2

# Kill SSE listener and check for notifications
kill $SSE_PID 2>/dev/null || true
sleep 0.5
kill -9 $SSE_PID 2>/dev/null || true
wait $SSE_PID 2>/dev/null || true

if grep -q 'notifications/tasks/status' /tmp/sse-notifications.txt 2>/dev/null; then
  echo -e "${GREEN}✓ Received notifications/tasks/status on SSE stream${NC}"
  grep 'notifications/tasks/status' /tmp/sse-notifications.txt | head -2 | sed 's/^data: //' | jq -S . 2>/dev/null
else
  echo -e "${YELLOW}✗ No notifications/tasks/status received (Phase 6 not implemented)${NC}"
fi

# ============================================================================
exercise 14 "Progress notifications (Phase 7) ✅"
# ============================================================================
cmd 'Open SSE stream, run 3-second computation, check for notifications/progress'
expect 'SSE stream receives progress notifications (1/3, 2/3, 3/3)'

# Start SSE listener in background
curl -s -N "$BASE" -H "$ACCEPT" -H "Mcp-Session-Id: $SESSION_ID" > /tmp/sse-progress.txt 2>&1 &
PROG_PID=$!
sleep 0.5

# Create a 3-second computation task
mcp "$BASE" -H "$SH" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":40,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":3,"label":"progress-test"},"task":{}}}'
sleep 4

# Kill SSE listener and check
kill $PROG_PID 2>/dev/null || true
sleep 0.5
kill -9 $PROG_PID 2>/dev/null || true
wait $PROG_PID 2>/dev/null || true

if grep -q 'notifications/progress' /tmp/sse-progress.txt 2>/dev/null; then
  PROG_COUNT=$(grep -c 'notifications/progress' /tmp/sse-progress.txt)
  echo -e "${GREEN}✓ Received $PROG_COUNT progress notifications${NC}"
  grep 'notifications/progress' /tmp/sse-progress.txt | head -3 | sed 's/^data: //' | jq -S . 2>/dev/null
else
  echo -e "${YELLOW}✗ No progress notifications received (Phase 7 not implemented in this server)${NC}"
fi

# ============================================================================
exercise 15 "Sub-task fan-out/join (Phase 8) 🔲"
# ============================================================================
echo -e "${YELLOW}Note: Phase 8 requires TaskContext.SpawnTool which is not yet implemented.${NC}"
echo -e "${YELLOW}Will be tested with a deploy tool (build + test in parallel).${NC}"
echo -e "${YELLOW}Tracked in panyam/mcpkit#281.${NC}"

# ============================================================================
exercise 16 "Cascade cancel (Phase 8) 🔲"
# ============================================================================
echo -e "${YELLOW}Note: Phase 8. Cancel parent → all children cancelled.${NC}"
echo -e "${YELLOW}Requires sub-task support (#281).${NC}"

# ============================================================================
echo ""
echo -e "${GREEN}═══════════════════════════════════════${NC}"
echo -e "${GREEN}  All exercises complete!${NC}"
echo -e "${GREEN}═══════════════════════════════════════${NC}"
echo ""
echo "Exercises 1-14: Phase 1-7 (implemented)"
echo "Exercises 15-16: Phase 8 (future — sub-task threading)"
echo ""
