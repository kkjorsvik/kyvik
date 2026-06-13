# Spending

## Overview

Kyvik tracks token usage and costs for every agent. Spending limits prevent runaway costs. Limits can be set globally and per-agent.

## Limit Types

| Limit | Default | Description |
|-------|---------|-------------|
| Daily USD | $10.00 | Maximum spend per calendar day |
| Monthly USD | $100.00 | Maximum spend per calendar month |
| Daily tokens | 0 (unlimited) | Maximum tokens per day |
| Monthly tokens | 0 (unlimited) | Maximum tokens per month |

A value of `0` means unlimited for token limits.

## What Happens When a Limit Is Hit

When an agent reaches a spending limit:

1. The current tool call completes (no mid-execution interruption)
2. The agent is paused — no further LLM calls are made
3. An audit entry is logged
4. A notification is sent (if notification threshold is configured)
5. An operator must either raise the limit or wait for the period to reset

## Per-Agent vs Global Limits

- **Global limits** are set in `kyvik.yaml` under `spending:`
- **Per-agent limits** override globals and are set in the agent configuration
- The most restrictive limit applies

## Spending Velocity (Circuit Breaker)

If an agent spends **50% of its daily budget within a 5-minute window**, the circuit breaker trips. This catches runaway loops where an agent burns through budget abnormally fast.

When the velocity breaker trips, the agent is quarantined and requires operator intervention. See [Security](security.md) for details on the circuit breaker.

## Notification Threshold

Configure `spending_threshold: 90` (percentage) to receive a notification when an agent reaches 90% of any budget limit. Set to `0` to disable.

## Tracking

Every LLM call records:
- Token count (input + output)
- Estimated cost in USD
- Provider and model used
- Timestamp

This data is available in the dashboard spending view and through the API.
