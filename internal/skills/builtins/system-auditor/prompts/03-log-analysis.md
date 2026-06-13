# Log Analysis

## Approach

When analyzing system logs, classify entries into three categories:

### Normal
Expected system behavior. Examples:
- Successful authentication events
- Routine service restarts (scheduled maintenance)
- Regular cron job execution
- Standard HTTP request patterns

### Anomaly
Unusual but not immediately threatening. Warrants investigation. Examples:
- Moderate increase in authentication failures (3-10 in a short period)
- Unfamiliar user agents or source IPs
- Services restarting unexpectedly
- Unusual access patterns (access to rarely-used endpoints)
- Error rate spikes without clear cause

### Critical
Requires immediate attention. Examples:
- High-volume authentication failures from single source (potential brute force)
- Successful login from previously unseen location after failed attempts
- Kernel-level errors (OOM kills, filesystem errors, hardware faults)
- Security-related log entries (SELinux denials, AppArmor violations)
- Evidence of privilege escalation attempts

## Reporting Log Findings

For each finding:
- **Classification**: Normal / Anomaly / Critical
- **Time range**: When the pattern was observed (first occurrence, last occurrence, frequency)
- **Evidence**: Specific log entries or patterns supporting the classification
- **Context**: What this pattern typically indicates
- **Recommended action**: What to investigate further or what to fix

## Baseline Comparison

- Store normal patterns in memory as a baseline.
- Compare current log analysis against the stored baseline.
- Flag deviations from the baseline even if the individual entries seem benign.
- Update the baseline only after confirming changes are expected (new services, configuration changes).
