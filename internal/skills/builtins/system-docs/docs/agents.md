# Agents

## What Is an Agent?

An agent is an isolated AI worker running inside Kyvik. Each agent has its own identity, message inbox/outbox, workspace directory, permission tier, model configuration, and sandbox. Every action an agent takes is audited.

## Agent States

An agent's runtime status can be one of:

| Status | Meaning |
|--------|---------|
| `stopped` | Not running, no goroutine active |
| `starting` | Goroutine launched, initializing |
| `running` | Processing messages normally |
| `paused` | Temporarily suspended, can be resumed |
| `error` | Encountered a problem, may need intervention |
| `quarantined` | Isolated due to safety concern — cannot process messages |
| `killed` | Forcefully terminated |

Operators set the **desired state** (`running`, `stopped`, `quarantined`, `killed`) and the runtime reconciles toward it.

## Lifecycle

1. **Created** — agent config saved to database, status is `stopped`
2. **Started** — operator sets desired state to `running`, an AgentRunner goroutine launches
3. **Running** — agent reads from inbox, calls LLM, executes tool calls through permission gates
4. **Paused/Stopped** — operator suspends or stops the agent gracefully
5. **Error** — runtime error occurs; agent may retry or wait for operator intervention
6. **Quarantined** — safety system (circuit breaker, prompt injection detector) isolates the agent
7. **Killed** — forceful termination, goroutine cancelled immediately

## Message Processing

Each agent has an inbox channel and outbox channel:

1. Messages arrive from channels (Web UI, Slack) or other agents (team delegation)
2. The agent's model router selects the appropriate model slot
3. The LLM generates a response, potentially with tool calls
4. Each tool call passes through the permission gate before execution
5. Tool results feed back to the LLM for the next turn
6. Final response goes to the outbox and back to the originating channel

## Workers and Concurrency

Each agent runs as a single goroutine (AgentRunner). Agents are independent — they don't share memory or state. Communication between agents uses the team messaging system (see [Teams](teams.md)).

The Kyvik core orchestrator manages all agent goroutines. Shutting down Kyvik gracefully stops all running agents.
