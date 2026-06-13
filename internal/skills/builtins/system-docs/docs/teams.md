# Teams

## What Is a Team?

A team is a group of agents that can communicate and coordinate. One agent is the **leader**; the rest are **members**. Teams enable multi-agent workflows where tasks are delegated, results collected, and work coordinated.

## Communication Modes

| Mode | How It Works |
|------|-------------|
| `leader-mediated` | All messages route through the leader. Members cannot talk to each other directly. The leader delegates tasks and collects results. |
| `open` | Any member can message any other member directly. The leader still coordinates but doesn't bottleneck communication. |

## Message Types

Internal team messages have a type and priority:

| Type | Purpose |
|------|---------|
| `message` | General communication |
| `task` | Work assignment from leader to member |
| `result` | Task completion response |
| `status` | Operational state update |

Priorities: `normal` (default) and `urgent` (for recalls and critical updates).

## Team Tools

Agents use team tools to communicate (all require `writer` tier minimum):

- **team:delegate** — leader sends a task to a specific member
- **team:broadcast** — send a message to all team members
- **team:status** — query a member's operational state
- **team:recall** — leader sends an urgent recall to a member

## Delegation Flow

1. Leader receives a user request
2. Leader breaks the task into sub-tasks
3. Leader uses `team:delegate` to assign each sub-task to a member
4. Members process their tasks and return results
5. Leader collects results and synthesizes a final response

## Paired Conversations

For structured two-agent dialogues, Kyvik supports **paired conversations**. Two agents engage in a back-and-forth exchange with defined turn-taking.

Paired conversation states: `active`, `paused`, `completed`, `stopped`.

Use cases: code review (writer + reviewer), debate (agent A + agent B), iterative refinement.

## Shared Context

Teams can have `SharedContext` enabled, allowing members to access a common context store. This is useful for teams working on related aspects of the same problem.
