# Models

## Providers

Kyvik supports four LLM providers:

| Provider | Name | Notes |
|----------|------|-------|
| OpenRouter | `openrouter` | Multi-model gateway, supports many models |
| Anthropic | `anthropic` | Direct Anthropic API access |
| OpenAI | `openai` | Direct OpenAI API access |
| Ollama | `ollama` | Local models, no API key needed |

All providers implement the same interface: `Complete()`, `Stream()`, `ListModels()`. Stop reasons are normalized across providers to `"end"`, `"tool_use"`, or `"max_tokens"`.

## Model Slots

Each agent can have multiple **model slots** — named configurations pointing to a specific provider and model:

```yaml
slots:
  - name: default
    provider: openrouter
    model: anthropic/claude-sonnet-4-20250514
  - name: heavy
    provider: anthropic
    model: claude-opus-4-20250514
  - name: fast
    provider: openrouter
    model: anthropic/claude-haiku-4-20250514
  - name: vision
    provider: openai
    model: gpt-4o
```

One slot is the **default** — used when no routing rule matches.

## Routing

The router decides which slot handles each message. Routing runs in priority order:

1. **Prefix trigger** — if enabled, the message format `slotname: message` routes directly to that slot. Example: `heavy: analyze this complex codebase`. Slot name matching is case-insensitive.

2. **Vision routing** — if the message has an image attachment and a `vision` slot is configured, it routes there automatically.

3. **Auto-classification** — if enabled, a dedicated classifier model analyzes the message and picks the best slot. Returns a confidence level (high/medium/low). Low confidence falls back to the default or fallback slot. Results are cached for 60 seconds per agent.

4. **Default** — falls through to the default slot.

## Configuration

Routing is configured per-agent:

- `AutoRoute` — enable LLM-based auto-classification
- `TriggerPrefix` — enable `slotname: message` prefix routing
- `ClassifierSlot` — which slot to use for auto-classification
- `FallbackSlot` — where low-confidence classifications go
- `DefaultSlot` — the catch-all slot

## Provider Credentials

Set via environment variables or the secrets vault:
- `KYVIK_OPENROUTER_API_KEY`
- `KYVIK_ANTHROPIC_API_KEY`
- `KYVIK_OPENAI_API_KEY`
- Ollama requires no API key (local)
