# Screenshots — system-monitor

Capture by running `RENDERER=basic-host make demo-app EXAMPLE=system-monitor`.

| File | What to capture |
|---|---|
| `01-dashboard.png` | System Monitor iframe showing the live dashboard: CPU / memory / disk gauges + per-process table. Updates as the iframe polls `poll-system-stats` every few seconds. |
