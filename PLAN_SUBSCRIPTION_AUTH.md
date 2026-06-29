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

Status: commits 1–18 complete (Anthropic + OpenAI/Codex subscription auth).

1. [x] **docs:** this plan file.
2. [x] **llm/oauth: PKCE primitives** — `pkce.go` (verifier/challenge S256) + tests.
3. [x] **llm/oauth: Anthropic OAuth endpoints** — authorize-URL builder, token
   exchange/refresh request shaping (pure functions, httptest) + tests.
4. [x] **llm/oauth: credential store** — read/write `credentials.json`, expiry
   accounting, redaction + tests (temp dir).
5. [x] **llm/oauth: refreshing TokenSource** — returns a valid token, refreshes when
   near expiry, persists via store + tests (fake clock/refresh fn, no sleeps).
6. [x] **llm/ant: Authorizer seam** — add `Authorizer` iface + `APIKeyAuth`; make
   `Service` use it; keep `X-API-Key` semantics identical. Tests assert headers.
   (Replaces `APIKey` per AGENTS rule 4 — no compat shims.)
7. [x] **llm/ant: OAuth authorizer** — `OAuthAuth` sets Bearer/beta/identity headers,
   drops x-api-key; `fromLLMRequest` injects Claude Code system prefix when
   required. Tests assert wire shape.
8. [x] **modelsources: Subscription source** — `Subscription(...)` producing
   OAuth-backed Anthropic services; wire precedence + tests.
9. [x] **cmd/shelley: `login` command** — `shelley login anthropic` runs the OAuth
   flow and writes the store; `logout`/`status` too. Tests for arg parsing and
   flow wiring (flow itself mocked).
10. [x] **wire-up in buildLLMModelSources + docs** — prefer subscription when logged
    in; update README/ARCHITECTURE; final integration test.
11. [ ] **(phase 2, optional)** OpenAI/Codex authorizer + login subcommand, same
    shape.

## Phase 2: OpenAI / Codex (ChatGPT subscription)

Confirmed transport details (Codex CLI public client):

- OAuth 2.0 + PKCE; client `app_EMoamEEZ73f0CkXaXp7hrann`.
- Authorize `https://auth.openai.com/oauth/authorize`,
  token `https://auth.openai.com/oauth/token`,
  redirect `http://localhost:1455/auth/callback`.
- The subscription backend is the Responses API at
  `https://chatgpt.com/backend-api/codex` (so the existing `ResponsesService`
  is the right client; only auth + base URL + identity differ).
- `Authorization: Bearer <access_token>` plus `chatgpt-account-id: <id>`, where
  the id comes from the `chatgpt_account_id` claim in the returned id_token JWT.
- First instruction/system block must be the Codex identity prompt.
- Same ToS risk as Anthropic; same opt-in isolation.

Commits:

12. [x] **llm/oauth: OpenAI OAuth endpoints** — authorize URL, code
    exchange/refresh, id_token account-id extraction + tests.
13. [x] **llm/oauth: OpenAI login flow + TokenSource AccountID** + tests.
14. [x] **llm/oai: Authorizer seam** on ResponsesService (replace APIKey with
    APIKeyAuth; chat oai.Service stays key-only, it can't do subscription) +
    tests.
15. [x] **llm/oai: OAuth/Codex authorizer** (Bearer + chatgpt-account-id +
    Codex identity instructions) + tests.
16. [x] **modelsources: Subscription serves OpenAI codex models** + tests.
17. [x] **cmd/shelley: login openai** (extend login/logout/status) + tests.
18. [x] **wire-up + docs**.

## Testing conventions honored

- No sleeps; inject clocks/refresh funcs.
- httptest servers assert exact headers/bodies.
- `go test ./llm/... ./models ./modelsources ./cmd/shelley` per commit.
- UI untouched until/unless a settings surface is added (separate later work).
