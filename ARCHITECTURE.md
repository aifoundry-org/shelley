Shelley is an agentic loop with tool use. See
https://sketch.dev/blog/agent-loop for an example of the idea.

When Shelley is started with "go run ./cmd/shelley" it starts a web server and
opens a sqlite database, and users interact with the ui built in ui/. (The
server itself is implemented in server/; cmd/shelley is a very thing shim.)

## Components

### ui/

TODO: A mobile-first UI.
Infrastructure:
  * pnpm
  * Typescript
  * esbuild
  * ESLint and eslint-typescript
  * VueJS
  * Jest

### db/

conversation(conversation_id, slug, user_initiated):
  
  Represents a single conversation.

message(conversation_id, message_id, type (agent/user/tool), llm_data (json), user_data (json), usage (json))

  Messages are visible in the UI and sent to the LLM as part of the 
  conversation. There may be both user-visible and llm-visible representations
  of messages.

The database is sqlite. We use sqlc to define queries and schema.

TODOX: Subagent/tool conversations are done with user_initiated=false.

### server/

The server serves the agent HTTP API and maintains active
conversations. The HTTP API is:

/conversations?limit=5000&offset=0
/conversations?q=search_term

  Returns conversations, either matching a query, or matching
  the paging requirements.

/conversation/<id>

  Returns all the messages within a conversation.

/conversation/<id>/stream

  Returns all the messages within a conversation and
  uses SSE to wait for updates.

/conversation/<id>/chat (POST)

  Injects a user message into the conversation


When a conversation is active (because it's had a message sent to it, or there
are stream subscribers), a Conversation struct is instantiated from the data,
and the server keeps a map of these. Each of these has a Loop struct to keep
track of the interaction with the llm.

## loop/

The core agentic loop.

## claudetool/

Various tools for the LLM.


## Provider authentication

Shelley talks to the LLMs using the llm/ library. Each provider client
(`llm/ant`, `llm/oai`, `llm/gem`) authenticates per request.

Auth is pluggable via per-provider `Authorizer` interfaces:

- Anthropic (`ant.Authorizer`): `ant.APIKeyAuth` sends `X-API-Key` (default);
  `ant.OAuthAuth` is Claude **subscription** auth — an OAuth bearer token plus
  the Claude Code identity headers/system prefix the subscription backend wants.
- OpenAI (`oai.Authorizer`, on `ResponsesService`): `oai.APIKeyAuth` sends a
  bearer key (default); `oai.OAuthAuth` is ChatGPT **subscription** auth — an
  OAuth bearer token plus the `chatgpt-account-id` header and the Codex identity
  instruction, pointed at the ChatGPT Codex backend URL.

The OAuth flows (PKCE), token store, and refreshing token sources live in
`llm/oauth`. Credentials are stored in `~/.config/shelley/credentials.json`
(0600), keyed by provider. `shelley login openai` uses the Codex **device-code**
flow ("Sign in with Device Code"): it prints a URL and a short code and polls
until the user approves in any browser — no localhost callback. `shelley login
anthropic` uses the browser flow where the user pastes back an authorization
code.

Model credentials are assembled in `modelsources/`. Sources are tried in
priority order: **Subscription** (if logged in) → exe.dev LLM integration →
gateway → env vars → predictable.

Use `shelley login <anthropic|openai>` to log in, `shelley login-status` to
check, and `shelley logout` to remove credentials.

> WARNING: Using subscription credentials from a non-official client is
> undocumented and may violate the provider's terms of service.

## Other

Logging happens with slog and the tint library.
