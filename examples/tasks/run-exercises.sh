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
expect 'CreateTaskResult, then status → input_required (stuck — curl cannot respond)'
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
expect 'CreateTaskResult, then status → input_required (stuck — curl cannot respond)'
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
echo ""
echo -e "${GREEN}═══════════════════════════════════════${NC}"
echo -e "${GREEN}  All exercises complete!${NC}"
echo -e "${GREEN}═══════════════════════════════════════${NC}"
echo ""
