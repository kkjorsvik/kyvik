# Security Checks

## Checklist

Run these checks during every audit:

### Network
- [ ] **Open ports** — List all listening ports. Flag ports exposed to all interfaces (0.0.0.0) vs. localhost only.
- [ ] **Unexpected listeners** — Compare against expected services. Flag any unknown processes with open ports.
- [ ] **Firewall rules** — Review iptables/nftables/ufw rules. Note any overly permissive rules.

### File Permissions
- [ ] **Sensitive files** — Check permissions on `/etc/shadow`, `/etc/passwd`, SSH keys, and application config files containing secrets.
- [ ] **World-writable files** — Flag any files or directories writable by all users, especially in system paths.
- [ ] **SUID/SGID binaries** — List binaries with setuid or setgid bits. Flag unexpected ones.

### Processes
- [ ] **Running as root** — List processes running as root. Flag any that do not require root privileges.
- [ ] **Unknown processes** — Flag processes with unfamiliar names, especially those with network connections.
- [ ] **Resource usage** — Note processes consuming excessive CPU, memory, or disk I/O.

### Authentication
- [ ] **Failed logins** — Count recent authentication failures. Flag patterns suggesting brute force attempts.
- [ ] **SSH configuration** — Check for password authentication enabled, root login allowed, and default port usage.
- [ ] **User accounts** — List accounts with shell access. Flag accounts with no recent login or expired passwords.

### Maintenance
- [ ] **Cron jobs** — List all scheduled tasks across all users. Flag jobs running as root or with broad permissions.
- [ ] **Environment variables** — Check for secrets stored in environment variables of running processes.
- [ ] **Log rotation** — Verify logs are being rotated and not consuming excessive disk space.
- [ ] **Disk usage** — Check filesystem usage. Flag partitions above 85% capacity.
