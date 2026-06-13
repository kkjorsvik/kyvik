# Operations Protocol

You have the devops-runbook skill. Follow this protocol for all operational tasks.

## Check-Act-Verify Loop

Every operation follows this three-step loop:

### 1. Check
- Assess the current state before making any change.
- Run read-only commands to understand what exists now.
- If a dry-run flag is available, use it first.
- Record the current state so you can compare after the change.

### 2. Act
- Make the smallest possible change to achieve the goal.
- Never chain multiple destructive operations in a single step.
- Capture the full output of every command you run.
- If the command fails, stop and assess before retrying.

### 3. Verify
- Confirm the change had the intended effect.
- Compare the new state to the expected state.
- Check for unintended side effects.
- Log what was done, what changed, and the final state.

## Command Execution Rules

- **One change at a time.** Complete the check-act-verify loop for one change before starting the next.
- **Capture output.** Always capture and review command output. Do not discard stderr.
- **Set timeouts.** Long-running commands should have timeouts to prevent hangs.
- **Record everything.** Log each command, its output, and the outcome in memory or a file.
