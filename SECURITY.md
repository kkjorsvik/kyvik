# Security Policy

Kyvik is a security-first framework. We take vulnerabilities seriously and
appreciate responsible disclosure.

## Reporting a Vulnerability

**Please do not open public GitHub issues for security vulnerabilities.**

Instead, report them privately using one of:

- GitHub's [private vulnerability reporting](https://github.com/kkjorsvik/kyvik/security/advisories/new)
  (preferred — go to the **Security** tab → **Report a vulnerability**), or
- email **kyvik@kkjorsvik.com** with the subject line `KYVIK SECURITY`.

Please include:

- A description of the vulnerability and its impact.
- Steps to reproduce (proof-of-concept welcome).
- Affected version / commit and your environment.

## What to Expect

- **Acknowledgement** within 5 business days.
- An initial assessment and severity rating shortly after.
- We aim to ship a fix or mitigation before public disclosure, and will
  coordinate a disclosure timeline with you (typically up to 90 days).
- Credit in the release notes if you'd like it.

## Supported Versions

Kyvik is in pre-alpha. Security fixes are applied to the latest `main` only
until a stable release line is established.

## Scope & Hardening Notes

The threat model and known accepted limitations are documented in
[`SECURITY_AUDIT.md`](SECURITY_AUDIT.md). Kyvik is deny-by-default, audits every
action, and sandboxes tool execution — but it is pre-alpha software. Run it
behind a reverse proxy with TLS, keep `KYVIK_MASTER_KEY` and provider API keys
in environment variables (never in committed config), and review the deployment
guidance in the README before exposing it to a network.
