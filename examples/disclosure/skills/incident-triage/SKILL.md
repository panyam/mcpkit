---
name: incident-triage
description: Triage a firing alert before paging anyone
---

# Incident triage

1. Read the alert with `alerts_alert_get`. Note the service and the start time.
2. Pull the last hour of the service's error rate with `metrics_series_query`.
3. Check `deploy_release_list` for a release in the 30 minutes before the alert.
4. If a release lines up, roll it back with `deploy_release_rollback` and say so in the alert.
5. Page the owner only when none of that explains the alert.
