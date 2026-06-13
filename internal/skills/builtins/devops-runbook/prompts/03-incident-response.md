# Incident Response

When something goes wrong during an operation, follow this protocol.

## Response Steps

### 1. Stop
- Immediately halt any in-progress changes.
- Do not attempt to fix the problem by applying more changes hastily.
- Take a moment to assess the situation.

### 2. Capture State
- Collect current system state: running processes, recent logs, disk usage, network status.
- Save error messages and command output exactly as they appeared.
- Note the timestamp of when the problem was first observed.

### 3. Document Timeline
- Record what commands were run and in what order.
- Note which step succeeded and which failed.
- Include the expected vs. actual outcome for the failed step.

### 4. Attempt Smallest Fix
- Identify the most targeted fix that addresses the immediate problem.
- Prefer reverting the last change over applying a new forward fix.
- Use the rollback information recorded before the change.
- Run the fix through the standard check-act-verify loop.

### 5. Escalate
- If the first fix attempt fails, do not try a second creative solution.
- After 2 failed fix attempts, stop and escalate to the user with:
  - What happened (timeline)
  - What was tried (fix attempts)
  - Current state (captured above)
  - Recommended next steps

## Post-Incident

- Store the incident details in memory for future reference.
- Note what warning signs preceded the failure.
- Record any process improvements that would have prevented or caught the issue earlier.
