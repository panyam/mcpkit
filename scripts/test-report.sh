#!/usr/bin/env bash
# Generate the testall HTML report with per-stage collapsible logs.
#
# Runner-agnostic: the root Makefile + justfile `test-report` recipes and
# scripts/testall.sh all call this the same way. Pure function of its inputs:
# reads per-stage logs from $REPORT_DIR/stage-<label>.log and writes
# $REPORT_DIR/report.html. The re-run column shows the leaf script that
# reproduces each stage (`bash <script>`), which is carried in the STAGES
# entry's 3rd field.
#
# Usage: test-report.sh <STAGES>
#   STAGES  space-separated "label:RESULT:script:elapsed" entries
# Env:
#   REPORT_DIR  output directory (default: tests/reports)
set -u

STAGES="${1:-}"
REPORT_DIR="${REPORT_DIR:-tests/reports}"

mkdir -p "$REPORT_DIR"
TIMESTAMP=$(date '+%Y-%m-%d %H:%M:%S')
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
R=$REPORT_DIR/report.html
echo '<!DOCTYPE html>' > "$R"
echo '<html><head><meta charset="utf-8"><title>MCPKit Test Report</title>' >> "$R"
echo '<style>' >> "$R"
echo 'body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; max-width: 900px; margin: 40px auto; padding: 0 20px; color: #333; }' >> "$R"
echo 'h1 { border-bottom: 2px solid #333; padding-bottom: 10px; }' >> "$R"
echo '.meta { color: #666; font-size: 14px; margin-bottom: 20px; }' >> "$R"
echo 'table { border-collapse: collapse; width: 100%; margin: 20px 0; }' >> "$R"
echo 'th, td { border: 1px solid #ddd; padding: 10px 14px; text-align: left; }' >> "$R"
echo 'th { background: #f5f5f5; font-weight: 600; }' >> "$R"
echo '.pass { color: #22863a; font-weight: 600; }' >> "$R"
echo '.fail { color: #cb2431; font-weight: 600; }' >> "$R"
echo '.skip { color: #6a737d; font-weight: 600; }' >> "$R"
echo '.info { color: #b08800; font-weight: 600; }' >> "$R"
echo '.summary-pass { background: #dcffe4; padding: 12px 20px; border-radius: 6px; font-size: 18px; }' >> "$R"
echo '.summary-fail { background: #ffdce0; padding: 12px 20px; border-radius: 6px; font-size: 18px; }' >> "$R"
echo 'details { margin: 8px 0; }' >> "$R"
echo 'summary { cursor: pointer; font-weight: 600; padding: 6px 0; }' >> "$R"
echo 'summary:hover { color: #0366d6; }' >> "$R"
echo 'pre { background: #f6f8fa; padding: 16px; border-radius: 6px; overflow-x: auto; font-size: 13px; max-height: 500px; overflow-y: auto; }' >> "$R"
echo 'code.cmd { background: #f0f0f0; padding: 2px 6px; border-radius: 3px; font-size: 13px; }' >> "$R"
echo '</style></head><body>' >> "$R"
echo "<h1>MCPKit Test Report</h1>" >> "$R"
echo "<div class='meta'>Branch: <strong>$BRANCH</strong> | Commit: <code>$COMMIT</code> | Date: $TIMESTAMP</div>" >> "$R"

PASS=0; FAIL=0; INFO=0
echo "<table><tr><th>Stage</th><th>Result</th><th>Re-run</th></tr>" >> "$R"
for entry in $STAGES; do
    STAGE=$(echo "$entry" | cut -d: -f1)
    RESULT=$(echo "$entry" | cut -d: -f2)
    TARGET=$(echo "$entry" | cut -d: -f3)
    if [ "$RESULT" = "PASS" ]; then
        echo "<tr><td><a href='#log-$STAGE'>$STAGE</a></td><td class='pass'>PASS</td><td><code class='cmd'>bash $TARGET</code></td></tr>" >> "$R"
        PASS=$((PASS+1))
    elif [ "$RESULT" = "SKIP" ]; then
        echo "<tr><td>$STAGE</td><td class='skip'>SKIP</td><td><code class='cmd'>bash $TARGET</code></td></tr>" >> "$R"
    elif [ "$RESULT" = "INFO" ]; then
        echo "<tr><td><a href='#log-$STAGE'>$STAGE</a></td><td class='info'>INFO</td><td><code class='cmd'>bash $TARGET</code></td></tr>" >> "$R"
        INFO=$((INFO+1))
    else
        echo "<tr><td><a href='#log-$STAGE'>$STAGE</a></td><td class='fail'>FAIL</td><td><code class='cmd'>bash $TARGET</code></td></tr>" >> "$R"
        FAIL=$((FAIL+1))
    fi
done
echo "</table>" >> "$R"

if [ $FAIL -eq 0 ] && [ $INFO -eq 0 ]; then
    echo "<div class='summary-pass'>All $PASS stages passed</div>" >> "$R"
elif [ $FAIL -eq 0 ]; then
    echo "<div class='summary-pass'>$PASS passed, $INFO informational (no failures)</div>" >> "$R"
else
    echo "<div class='summary-fail'>$PASS passed, $FAIL failed, $INFO informational</div>" >> "$R"
fi

echo "<h2>Stage Logs</h2>" >> "$R"
for entry in $STAGES; do
    STAGE=$(echo "$entry" | cut -d: -f1)
    RESULT=$(echo "$entry" | cut -d: -f2)
    LOGFILE=$REPORT_DIR/stage-$STAGE.log
    OPEN=""; if [ "$RESULT" = "FAIL" ] || [ "$RESULT" = "INFO" ]; then OPEN=" open"; fi
    case "$RESULT" in
        PASS) CLS=pass ;;
        INFO) CLS=info ;;
        SKIP) CLS=skip ;;
        *) CLS=fail ;;
    esac
    echo "<details id='log-$STAGE'$OPEN><summary class='$CLS'>$STAGE — $RESULT</summary><pre>" >> "$R"
    if [ -f "$LOGFILE" ]; then
        sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g' "$LOGFILE" >> "$R"
    else
        echo "(no log file found)" >> "$R"
    fi
    echo "</pre></details>" >> "$R"
done
echo "</body></html>" >> "$R"
