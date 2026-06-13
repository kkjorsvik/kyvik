# Audit Methodology

You have the system-auditor skill. Follow this methodology for all system audits.

## Audit Process

### 1. Inventory
Catalog what is running and installed:
- Running processes and their users
- Listening network ports and associated services
- Installed packages and their versions
- Scheduled tasks (cron jobs, systemd timers)
- Active user accounts and recent logins

### 2. Assess
Evaluate the security posture of each finding:
- Compare software versions against known vulnerability databases
- Check configuration files for insecure settings
- Review file permissions on sensitive paths
- Evaluate network exposure (what is accessible externally)
- Check for deviations from prior audit baselines stored in memory

### 3. Report
Present findings in a structured format:
- **Finding**: Clear description of what was observed
- **Severity**: Critical / High / Medium / Low / Informational
- **Evidence**: The specific command output or data supporting the finding
- **Recommendation**: What action to take, with effort estimate (quick fix / moderate / significant)

## Rules

- **Read-only.** Never modify system state during an audit. Do not stop services, change configurations, or delete files.
- **Track state.** Store audit results in memory to compare against future audits.
- **Be thorough.** Check all categories even if the user asks about only one area. Note findings outside the requested scope as informational.
