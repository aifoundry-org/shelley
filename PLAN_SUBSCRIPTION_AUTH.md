# Plan: Subscription (OAuth) auth for AI providers

Goal: let Shelley authenticate to AI providers using **subscription**
credentials (Claude Pro/Max OAuth, and later ChatGPT/Codex OAuth) instead of
only inference API keys.

## Current state (analysis)

Auth is API-key only, set inline in each provider client:

- `llm/ant/ant.go:1146` — `X-API-Key: <key>` + `Anthropic-Version`.
- `llm/oai/oai_responses.go:612` — `Authorization: Bearer <key>`.
- `llm/oai/oai.go` / `llm/gem/gem.go` — analogous.

Keys flow in from `modelsources` (`Gateway`/`Env`/`LLMIntegration`/`Predictable`)
through `models.Model.Build(baseURL, apiKey, httpc)` factories
(`models/models.go:141-175`) and from DB-backed custom models
(`models/models.go:654`). Each `Service` has a bare `APIKey string`.

## What subscription auth requires (Anthropic, the first target)

Claude Pro/Max uses OAuth, not API keys. Confirmed transport details:

- `Authorization: Bearer <access_token>` and **no** `x-api-key` header.
- `anthropic-beta: oauth-2025-04-20,claude-code-20250219` (merged with any
  existing beta values).
- Identity headers `x-app: cli` and `user-agent: claude-cli/...`.
- First system block must be the Claude Code identity string
  ("You are Claude Code, Anthropic's official CLI for Claude.").
- Access tokens are short-lived; a refresh token mints new ones.
- Tokens are obtained via OAuth 2.0 + PKCE against claude.ai/console.

### Risk callout (must keep visible to the user)

Anthropic may reject these tokens from non-Claude-Code clients
("This credential is only authorized for use with Claude Code") and may route
tool-carrying OAuth traffic to a disabled overage lane. This is undocumented,
unstable, and may violate provider ToS / risk account suspension. We isolate it
behind an explicit opt-in and surface errors clearly.

## Design

Introduce a small, provider-local **authorizer** seam. Rather than a bare
`APIKey string`, an Anthropic `Service` carries an `Authorizer` that knows how
to (a) set auth headers per attempt (so it can refresh tokens), (b) contribute
extra `anthropic-beta` values, and (c) declare whether Claude Code identity
(system prefix) is required.

```go
// llm/ant
type Authorizer interface {
    SetAuth(ctx context.Context, h http.Header) error // per-attempt; may refresh
    BetaHeaders() []string                            // extra anthropic-beta tokens
    RequiresClaudeCodeIdentity() bool                 // OAuth needs the CC system prefix
}
```

Two implementations:
- `APIKeyAuth{Key string}` — today's `X-API-Key` behavior.
- `OAuthAuth{TokenSource}` — Bearer + beta + identity headers, refreshing via a
  `TokenSource`.

A shared `llm/oauth` package holds: PKCE helpers, the Anthropic OAuth flow
(authorize URL + code exchange + refresh), an on-disk credential store
(`credentials.json` in the config dir), and a refreshing `TokenSource`.

The OpenAI/Codex path mirrors this in a later phase with its own authorizer.

## Commit plan (each small, reviewable, tests-first)

Each feature commit is preceded by a test commit (or includes tests written
first in the same commit where splitting would not compile).

1. **docs:** this plan file. (you're reviewing it)
2. **llm/oauth: PKCE primitives** — `pkce.go` (verifier/challenge S256) + tests.
3. **llm/oauth: Anthropic OAuth endpoints** — authorize-URL builder, token
   exchange/refresh request shaping (pure functions, httptest) + tests.
4. **llm/oauth: credential store** — read/write `credentials.json`, expiry
   accounting, redaction + tests (temp dir).
5. **llm/oauth: refreshing TokenSource** — returns a valid token, refreshes when
   near expiry, persists via store + tests (fake clock/refresh fn, no sleeps).
6. **llm/ant: Authorizer seam** — add `Authorizer` iface + `APIKeyAuth`; make
   `Service` use it; keep `X-API-Key` semantics identical. Tests assert headers.
   (Replaces `APIKey` per AGENTS rule 4 — no compat shims.)
7. **llm/ant: OAuth authorizer** — `OAuthAuth` sets Bearer/beta/identity headers,
   drops x-api-key; `fromLLMRequest` injects Claude Code system prefix when
   required. Tests assert wire shape.
8. **modelsources: Subscription source** — `Subscription(...)` producing
   OAuth-backed Anthropic services; wire precedence + tests.
9. **cmd/shelley: `login` command** — `shelley login anthropic` runs the OAuth
   flow and writes the store; `logout`/`status` too. Tests for arg parsing and
   flow wiring (flow itself mocked).
10. **wire-up in buildLLMModelSources + docs** — prefer subscription when logged
    in; update README/ARCHITECTURE; final integration test.
11. **(phase 2, optional)** OpenAI/Codex authorizer + login subcommand, same
    shape.

## Testing conventions honored

- No sleeps; inject clocks/refresh funcs.
- httptest servers assert exact headers/bodies.
- `go test ./llm/... ./models ./modelsources ./cmd/shelley` per commit.
- UI untouched until/unless a settings surface is added (separate later work).
