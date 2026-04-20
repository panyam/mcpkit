#!/bin/bash
# Side-by-side comparison: mcpkit tasks vs TS SDK tasks
# Compares wire format and behavior of both implementations.
#
# Usage:
#   ./test-side-by-side.sh
#
# Prerequisites:
#   - Go server: this directory (examples/tasks)
#   - TS server: npm install in this directory (first time only)

set -e

GO_PORT=8090
TS_PORT=8091
ACCEPT="Accept: application/json, text/event-stream"
CT="Content-Type: application/json"
CLEANUP_PIDS=()
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

cleanup() {
  for pid in "${CLEANUP_PIDS[@]}"; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null
}
trap cleanup EXIT

if [ ! -d "$SCRIPT_DIR/node_modules/@modelcontextprotocol" ]; then
  echo "Installing TS SDK dependencies..."
  cd "$SCRIPT_DIR" && npm install
fi

# Helper: extract JSON from SSE or plain response
parse_response() {
  local raw="$1"
  local json
  json=$(echo "$raw" | grep '^data:' | tail -1 | sed 's/^data: //')
  if [ -z "$json" ]; then
    echo "$raw"
  else
    echo "$json"
  fi
}

echo "=========================================="
echo "  Stage 1: Starting servers"
echo "=========================================="
echo ""

cd "$SCRIPT_DIR"
go run . -addr ":$GO_PORT" 2>&1 &
CLEANUP_PIDS+=($!)

PORT=$TS_PORT node "$SCRIPT_DIR/ts-reference-server.mjs" 2>&1 &
CLEANUP_PIDS+=($!)

sleep 2
echo ""
echo "Go on :$GO_PORT, TS on :$TS_PORT"
echo ""

echo "=========================================="
echo "  Stage 2: Initialize sessions"
echo "=========================================="
echo ""

INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{"elicitation":{},"sampling":{}},"clientInfo":{"name":"test","version":"1.0"}}}'
NOTIF='{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'

echo "--- Go ---"
GO_INIT_RAW=$(curl -s -D /tmp/go_h.txt "http://localhost:$GO_PORT/mcp" -H "$CT" -H "$ACCEPT" -d "$INIT")
GO_SID=$(grep -i 'mcp-session-id' /tmp/go_h.txt | head -1 | awk '{print $2}' | tr -d '\r\n')
echo "Session: $GO_SID"
parse_response "$GO_INIT_RAW" | python3 -m json.tool 2>/dev/null || echo "$GO_INIT_RAW"
curl -s "http://localhost:$GO_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $GO_SID" -d "$NOTIF" > /dev/null 2>&1
echo ""

echo "--- TS ---"
TS_INIT_RAW=$(curl -s -D /tmp/ts_h.txt "http://localhost:$TS_PORT/mcp" -H "$CT" -H "$ACCEPT" -d "$INIT")
TS_SID=$(grep -i 'mcp-session-id' /tmp/ts_h.txt | head -1 | awk '{print $2}' | tr -d '\r\n')
echo "Session: $TS_SID"
parse_response "$TS_INIT_RAW" | python3 -m json.tool 2>/dev/null || echo "$TS_INIT_RAW"
curl -s "http://localhost:$TS_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $TS_SID" -d "$NOTIF" > /dev/null 2>&1
echo ""

echo "=========================================="
echo "  Stage 3: tools/list"
echo "=========================================="
echo ""

echo "--- Go ---"
GO_TL=$(curl -s "http://localhost:$GO_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $GO_SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}')
parse_response "$GO_TL" | python3 -m json.tool 2>/dev/null || echo "$GO_TL"
echo ""

echo "--- TS ---"
TS_TL=$(curl -s "http://localhost:$TS_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $TS_SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}')
parse_response "$TS_TL" | python3 -m json.tool 2>/dev/null || echo "$TS_TL"
echo ""

echo "=========================================="
echo "  Stage 4: greet (sync, no task hint)"
echo "=========================================="
echo ""

echo "--- Go ---"
GO_G=$(curl -s "http://localhost:$GO_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $GO_SID" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"greet","arguments":{"name":"World"}}}')
parse_response "$GO_G" | python3 -m json.tool 2>/dev/null || echo "$GO_G"
echo ""

echo "--- TS ---"
TS_G=$(curl -s "http://localhost:$TS_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $TS_SID" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"greet","arguments":{"name":"World"}}}')
parse_response "$TS_G" | python3 -m json.tool 2>/dev/null || echo "$TS_G"
echo ""

echo "=========================================="
echo "  Stage 5: slow_compute (async, task hint)"
echo "=========================================="
echo ""

echo "--- Go ---"
GO_CT_RAW=$(curl -s "http://localhost:$GO_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $GO_SID" \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":3,"label":"pi"},"task":{}}}')
GO_CT=$(parse_response "$GO_CT_RAW")
echo "$GO_CT" | python3 -m json.tool 2>/dev/null || echo "$GO_CT"
GO_TID=$(echo "$GO_CT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('result',{}).get('task',{}).get('taskId','MISSING'))" 2>/dev/null || echo "PARSE_FAILED")
echo "taskId: $GO_TID"
echo ""

echo "--- TS ---"
TS_CT_RAW=$(curl -s "http://localhost:$TS_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $TS_SID" \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":3,"label":"pi"},"task":{}}}')
TS_CT=$(parse_response "$TS_CT_RAW")
echo "$TS_CT" | python3 -m json.tool 2>/dev/null || echo "$TS_CT"
TS_TID=$(echo "$TS_CT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('result',{}).get('task',{}).get('taskId','MISSING'))" 2>/dev/null || echo "PARSE_FAILED")
echo "taskId: $TS_TID"
echo ""

echo "=========================================="
echo "  Stage 6: tasks/get (poll status)"
echo "=========================================="
echo ""
sleep 1

echo "--- Go ---"
GO_G_RAW=$(curl -s "http://localhost:$GO_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $GO_SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":5,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$GO_TID\"}}")
parse_response "$GO_G_RAW" | python3 -m json.tool 2>/dev/null || echo "$GO_G_RAW"
echo ""

echo "--- TS ---"
TS_G_RAW=$(curl -s "http://localhost:$TS_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $TS_SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":5,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$TS_TID\"}}")
parse_response "$TS_G_RAW" | python3 -m json.tool 2>/dev/null || echo "$TS_G_RAW"
echo ""

echo "=========================================="
echo "  Stage 7: tasks/list"
echo "=========================================="
echo ""

echo "--- Go ---"
GO_L_RAW=$(curl -s "http://localhost:$GO_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $GO_SID" \
  -d '{"jsonrpc":"2.0","id":6,"method":"tasks/list","params":{}}')
parse_response "$GO_L_RAW" | python3 -m json.tool 2>/dev/null || echo "$GO_L_RAW"
echo ""

echo "--- TS ---"
TS_L_RAW=$(curl -s "http://localhost:$TS_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $TS_SID" \
  -d '{"jsonrpc":"2.0","id":6,"method":"tasks/list","params":{}}')
parse_response "$TS_L_RAW" | python3 -m json.tool 2>/dev/null || echo "$TS_L_RAW"
echo ""

echo "=========================================="
echo "  Stage 8: tasks/result (wait for completion)"
echo "=========================================="
echo ""
sleep 3

echo "--- Go ---"
GO_R_RAW=$(curl -s "http://localhost:$GO_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $GO_SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"tasks/result\",\"params\":{\"taskId\":\"$GO_TID\"}}")
parse_response "$GO_R_RAW" | python3 -m json.tool 2>/dev/null || echo "$GO_R_RAW"
echo ""

echo "--- TS ---"
TS_R_RAW=$(curl -s "http://localhost:$TS_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $TS_SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"tasks/result\",\"params\":{\"taskId\":\"$TS_TID\"}}")
parse_response "$TS_R_RAW" | python3 -m json.tool 2>/dev/null || echo "$TS_R_RAW"
echo ""

echo "=========================================="
echo "  Stage 9: tasks/cancel"
echo "=========================================="
echo ""

# Create new tasks to cancel
GO_CAN_RAW=$(curl -s "http://localhost:$GO_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $GO_SID" \
  -d '{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":30,"label":"cancel-me"},"task":{}}}')
GO_CAN_TID=$(parse_response "$GO_CAN_RAW" | python3 -c "import sys,json; print(json.load(sys.stdin).get('result',{}).get('task',{}).get('taskId',''))" 2>/dev/null)

TS_CAN_RAW=$(curl -s "http://localhost:$TS_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $TS_SID" \
  -d '{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":30,"label":"cancel-me"},"task":{}}}')
TS_CAN_TID=$(parse_response "$TS_CAN_RAW" | python3 -c "import sys,json; print(json.load(sys.stdin).get('result',{}).get('task',{}).get('taskId',''))" 2>/dev/null)

echo "--- Go cancel ---"
GO_C_RAW=$(curl -s "http://localhost:$GO_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $GO_SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":9,\"method\":\"tasks/cancel\",\"params\":{\"taskId\":\"$GO_CAN_TID\"}}")
parse_response "$GO_C_RAW" | python3 -m json.tool 2>/dev/null || echo "$GO_C_RAW"
echo ""

echo "--- TS cancel ---"
TS_C_RAW=$(curl -s "http://localhost:$TS_PORT/mcp" -H "$CT" -H "$ACCEPT" -H "Mcp-Session-Id: $TS_SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":9,\"method\":\"tasks/cancel\",\"params\":{\"taskId\":\"$TS_CAN_TID\"}}")
parse_response "$TS_C_RAW" | python3 -m json.tool 2>/dev/null || echo "$TS_C_RAW"
echo ""

echo "=========================================="
echo "  Wire Format Comparison Summary"
echo "=========================================="
echo ""
echo "greet (sync):      Both return {content: [{type:'text', text:'Hello, World!'}]}"
echo "CreateTaskResult:  Both nest under 'task' key"
echo "tasks/get:         Both flat (no wrapper)"
echo "tasks/list:        Both return {tasks: [...]}"
echo "tasks/result:      Both return ToolResult with _meta[related-task]"
echo "tasks/cancel:      Both return flat cancelled task"
echo ""
echo "Expected differences:"
echo "  - TTL: Go=300000ms, TS=null (unlimited)"
echo "  - taskId format: Go='task-<hex>', TS='<uuid>'"
echo "  - Timestamps: Go=seconds, TS=milliseconds"
echo ""
