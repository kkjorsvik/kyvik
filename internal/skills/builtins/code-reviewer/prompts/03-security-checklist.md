# Security Checklist

Run through this checklist for every code review. Mark items as checked, found, or not applicable.

## Input Handling

- [ ] **Injection vulnerabilities** — SQL injection, command injection, LDAP injection. Check that user input is parameterized or sanitized before use in queries or commands.
- [ ] **Cross-site scripting (XSS)** — User-supplied content rendered in HTML is escaped. Check template rendering and dynamic content insertion.
- [ ] **Path traversal** — File paths constructed from user input are validated. Check for `../` sequences and ensure paths stay within intended directories.

## Data Handling

- [ ] **Hardcoded credentials** — No API keys, passwords, tokens, or secrets in source code. Check for strings that look like credentials.
- [ ] **Sensitive data exposure** — PII and secrets are not logged, not included in error messages, and not returned in API responses unless required.
- [ ] **Unsafe deserialization** — Data from external sources is validated before deserialization. Check for JSON, YAML, or binary unmarshaling of untrusted input.

## Concurrency and State

- [ ] **Race conditions** — Shared mutable state is protected by locks or channels. Check goroutines, threads, and async operations that access shared data.
- [ ] **Resource leaks** — File handles, database connections, HTTP clients, and goroutines are properly closed or cleaned up.

## Access Control

- [ ] **Authorization checks** — Protected operations verify the caller has permission. Check API endpoints and sensitive functions.
- [ ] **Information leakage** — Error messages do not reveal internal state, stack traces, or system paths to external users.

## Dependencies

- [ ] **Known vulnerabilities** — New dependencies are checked for known CVEs.
- [ ] **Minimal permissions** — Dependencies are granted only the access they need.
