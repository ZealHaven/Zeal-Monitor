# Plan: Configurable LLM provider and models

**Status:** implemented (2026-10-24) · **Scope:** backend + frontend + docs

## 1. Goal

Make the chat agent's LLM provider and model configurable from Monitor:

- Pick a provider and model from the models the running OpenCode server exposes.
- Add/replace/remove a provider API key and optional base URL without editing
  code or restarting Monitor by hand.
- Keep working with zero configuration: the current `opencode-go/beam-free`
  default remains the fallback.
- Never leak secrets through the API, UI, logs, or git.

Non-goals (v1): multi-user profiles, per-message model routing, hosted
credential vaults, model fine-tuning, automatic provider OAuth device flows.

## 2. Current state

| Piece | Today |
|---|---|
| Model | Hardcoded in `backend/main/internal/harness/harness.go:173` (`"model": "opencode-go/beam-free"`) |
| Provider | Implied by the model prefix; no credential config |
| Serve config | `OPENCODE_CONFIG_CONTENT` built once per process from the prompt file + hardcoded agent/model |
| Serve lifecycle | `Manager.Start` supervises one process; no restart/reload path |
| Settings API | None |
| Settings UI | None; chat dock has no model indicator |
| Config | `.env` for ports/paths only; `MONITOR_TOKEN` exists in `.env` but is unused |
| Discovery | OpenCode exposes `GET /api/provider` and `GET /api/model` (V2), authenticated with the serve password |

Two additional facts confirmed against OpenCode 2.1.2:

- `opencode run` supports `--model provider/model#variant`, so model changes
  can apply per message without restarting the server.
- The harness currently uses deprecated V1 config keys (`agent`, `permission`).
  The V2 equivalents are `agents`, `permissions`, and `providers`
  (`provider` is deprecated). This should be migrated while the config builder
  is rewritten.

## 3. Design

### 3.1 Configuration precedence

```
compiled default  <  config file  <  environment  <  (UI writes the config file)
```

- **Config file:** `$MONITOR_CONFIG`, else
  `${XDG_CONFIG_HOME:-$HOME/.config}/monitor/config.json`, mode `0600`,
  parent dir `0700`.
- **Environment:** `MONITOR_MODEL`, `MONITOR_LLM_PROVIDER_*` keys, plus the
  standard provider variables (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, …)
  which OpenCode already understands. Env wins and the UI shows the
  field as read-only with a "set by environment" badge.
- **Default:** `opencode-go/beam-free`, preserving today's behavior when no
  config exists.

### 3.2 Config schema

```jsonc
{
  "version": 1,
  "llm": {
    // Full OpenCode reference: provider/model or provider/model#variant
    "model": "opencode-go/beam-free",
    "providers": {
      "anthropic": {
        "apiKey": "sk-ant-...",          // optional; omit to use OpenCode auth/env
        "baseURL": "",                    // optional endpoint override
        "name": "",                       // optional custom provider
        "package": "",                    // e.g. @opencode/ai/providers/openai-compatible
        "headers": {},                    // optional
        "models": {}                      // optional custom model definitions
      }
    }
  }
}
```

Only explicitly configured providers are injected into
`OPENCODE_CONFIG_CONTENT`. Providers connected through
`opencode auth login` keep working untouched.

### 3.3 How changes are applied

| Change | Mechanism | Restart? |
|---|---|---|
| Model / variant | Append `--model <ref>` to `opencode run`; also set the default in serve config | No — next message |
| Provider API key, base URL, package, custom models | Rebuild `OPENCODE_CONFIG_CONTENT` and restart `opencode serve` | Yes (~1–2 s) |
| Prompt / agent definition | Rebuild config on restart | Yes |

Restart safety:

- `Manager` gets a per-process ready channel (replacing the current one-shot
  `sync.Once`) and a `runMu sync.RWMutex`.
- Chat `Run` holds `RLock`; provider changes take `Lock`.
- If a chat is in flight, the settings handler returns `409 chat_in_progress`
  instead of killing the stream. The UI shows "finish or cancel the current
  chat, then save".
- The supervisor loop rebuilds config from the store on every (re)start, so a
  config file edit followed by `systemctl restart monitord` also works.

### 3.4 Model discovery

`monitord` proxies the running serve's catalog so the browser never needs the
serve password:

- `GET /api/provider` → provider metadata/activation
- `GET /api/model` → model list (`providerID`, `modelID`, variants, cost, limits, status)

Merge and cache for 60 s; invalidate after a restart. Return a stable shape:

```json
{
  "providers": [
    {
      "id": "anthropic",
      "name": "Anthropic",
      "connected": true,
      "models": [
        {
          "ref": "anthropic/claude-sonnet-5-5",
          "name": "Claude Sonnet 5.5",
          "context": 200000,
          "output": 32000,
          "status": "active",
          "variants": ["high"]
        }
      ]
    }
  ],
  "current": "opencode-go/beam-free",
  "source": "opencode"
}
```

If the serve is starting or unreachable, return the current model and an empty
catalog with `"source": "unavailable"`. The UI then offers a manual
`provider/model` text field (format-validated) so a first-time provider can be
configured before its catalog loads.

### 3.5 Bootstrapping a new provider

Provider catalogs only include credentials known to the serve, so the UX is
two steps, made explicit in the UI:

1. **Connect** — enter provider key/base URL, save. This restarts the serve and
   refreshes discovery.
2. **Choose** — select a model from the now-populated list (or type a ref).
   Applies to the next message without another restart.

Saving a syntactically valid model together with a new key is also allowed, so
one save can do both when the user already knows the model ref.

### 3.6 Secrets handling

- File mode `0600`, atomic write (`tmp` → `fsync` → `rename`).
- `GET` responses never include keys:
  `api_key_set`, `api_key_hint` (`••••4f2a`), `source` (`file|env|opencode`).
- `PUT` semantics: omit `apiKey` = keep; `""` = clear; non-empty = replace.
- Keys are never logged. Settings logs include provider, model, and
  `key_action=set|cleared`, nothing else.
- `.gitignore` already excludes `.env`; the XDG config lives outside the repo.
- CORS currently allows `*` and advertises only `GET, POST, OPTIONS`; add `PUT`
  and restrict the default to same-origin (`MONITOR_CORS_ORIGIN` unchanged as
  the escape hatch). Settings endpoints are state-changing, so the preflight
  must be correct.

## 4. Implementation

### Phase 1 — Settings store (new package `internal/settings`)

- [x] `settings.LLM`, `settings.Provider`, `settings.Store` with `Load`,
      `Get`, `Update`, `Save` (atomic, `0600`), guarded by a mutex.
- [x] `Resolve()` applies defaults → file → env and records the source of each
      field for the UI.
- [x] `OpenCodeConfig(prompt string) map[string]any`: emits V2 keys —
      `model`, `agents.monitor` (`system`, `description`, `mode`, `permissions`),
      `providers.<id>` only for configured entries.
- [x] Validation: model ref regex `^[^/#]+/[^#]+(?:#[^#]+)?$`, provider ID
      charset, base URL scheme (`http`/`https`), key length cap.
- [x] Redaction helpers used by the API and logs.
- [x] Wire `cmd/monitord/main.go`: add `--config` (default
      `$MONITOR_CONFIG`, else the XDG path), load the store before
      `harness.New`, and log the resolved path — never its contents.
- [x] Unit tests: precedence, redaction, atomic write/permissions, config
      snapshot, invalid input.

### Phase 2 — Harness

- [x] `harness.New(..., store *settings.Store)`; `buildConfig` delegates to
      `store.OpenCodeConfig(readPrompt())`.
- [x] `Run(ctx, sessionID, message, model string, onEvent)` appends
      `--model` when non-empty; keep the configured model as the serve default.
- [x] `Restart(ctx)` / `Reload()`: kill child, rebuild config, wait for the new
      process's ready channel (generation counter to avoid returning on the old
      one).
- [x] `APIRequest(ctx, method, path)` helper with Basic auth (`opencode:<pw>`)
      for provider/model discovery.
- [x] Migrate deprecated V1 keys (`agent`→`agents`, `permission`→`permissions`)
      **after** verifying the accepted rule shape on the installed version
      (`{action:"bash"|"shell", resource:"*", effect:"allow"}`); add a config
      snapshot test. If verification raises doubt, ship the migration as a
      separate follow-up rather than blocking this feature.
- [x] Tests: `--model` present/absent, restart rebuilds config, readiness
      resets, `TryLock` returns `ErrChatInFlight`.

### Phase 3 — API

- [x] `GET /api/settings/llm` — effective settings, sources, env override list,
      serve status (`ready`, `url`), redacted provider list.
- [x] `PUT /api/settings/llm` — partial update; returns
      `{settings, applied:"immediate"|"restart", restarted:bool}`; `409` when a
      chat is running and a restart is required.
- [x] `POST /api/settings/llm/reload` — manual serve restart after
      `opencode auth login` changes.
- [x] `GET /api/llm/models` — discovery proxy + cache + `source`.
- [x] Optional `POST /api/settings/llm/test` — verify the model resolves and,
      when explicitly requested, run a one-shot "reply OK" probe with a
      no-tool agent and a 30 s timeout.
- [x] Validation errors are `400` with a machine-readable `error` code.
- [x] Router + CORS updates; request-body limit (e.g. 64 KiB) so keys aren't
      streamed unbounded.
- [x] Handler tests with a fake store/harness; key never appears in responses.

### Phase 4 — Frontend

- [x] `src/lib/api.ts`: `fetchLLMSettings`, `saveLLMSettings`,
      `fetchLLMModels`, `reloadAgent`; use the shared `API` base (also fixes
      the hardcoded `127.0.0.1:9050` in `routes/audit/+page.ts`).
- [x] `src/lib/types.ts`: settings/model types mirroring the API.
- [x] New route `/settings` (`+page.svelte`, `+page.ts`):
      - status card (agent ready/starting/error, current model, source)
      - provider list with configured/env/OpenCode badges and key editor
      - model picker grouped by provider, searchable, shows context/cost and
        variant select
      - manual model-ref fallback when discovery is unavailable
      - save bar with dirty/dirty-state, inline validation, and a clear
        "applies now" vs "restarts the agent" hint
- [x] `+layout.svelte`: add **Settings** to the nav.
- [x] `ChatDock.svelte`: show the active model in the header (truncated chip,
      full ref in `title`), linking to `/settings`; refresh the chip after a
      settings save and show the serve's model in an error state.
- [x] `svelte-check` clean; responsive at the dock's 570 px width and mobile.

Wireframe:

```
┌ Settings ────────────────────────────────────────────────┐
│ Agent  ● ready   http://127.0.0.1:9060   model: opencode-go/beam-free │
├ LLM provider ────────────────────────────────────────────┤
│ ● Anthropic        configured ••••4f2a      [change] [x] │
│ ○ OpenAI           set by env OPENAI_API_KEY             │
│ ○ OpenCode Go      via OpenCode auth                     │
│ + Add provider key [__________________] [base URL ____]  │
├ Model ───────────────────────────────────────────────────┤
│ [ anthropic/claude-sonnet-5-5            ▾ ]  variant [ ]│
│   200k context · $3/$15 per Mtok · active                │
│ ☑ applies to the next message                            │
├──────────────────────────────────────────────────────────┤
│ unsaved changes                       [Reset] [Save]     │
└──────────────────────────────────────────────────────────┘
```

### Phase 5 — Docs and configuration

- [x] `.env.example`: `MONITOR_CONFIG`, `MONITOR_MODEL`, commented provider
      key examples.
- [x] `README.md`: new "LLM configuration" section (UI flow, file location,
      precedence, restart behavior, security note).
- [x] `docs/LOCAL_FIRST.md`: mention that no key is needed if OpenCode is
      already connected.
- [x] Note in `ops/systemd/monitord.service` that settings are per-user XDG
      state (no unit change required unless `MONITOR_CONFIG` is set).

## 5. Acceptance tests

- [x] Fresh clone with no config file and no env still chats with
      `opencode-go/beam-free`.
- [x] `/settings` lists providers/models from the running serve; the current
      model is selected.
- [x] Saving an API key for a new provider restarts the serve, and the model
      list then includes that provider.
- [x] Selecting a model takes effect on the next message; the spawned
      `opencode run` command contains `--model <ref>`.
- [ ] Changing a provider while a chat streams returns `409` and the stream
      completes.
- [x] `GET /api/settings/llm` and logs contain no key material; config file is
      `0600`.
- [x] `MONITOR_MODEL` overrides the file and the UI shows it as locked (unit-tested).
- [ ] Mock OpenAI-compatible provider (test server exposing
      `/v1/chat/completions` SSE) can be added, selected, and used end-to-end.
      Gate this integration test behind `MONITOR_E2E_OPENCODE=1`.
- [x] `just check` and `just test` pass.

## 6. Risks and open questions

- **Credential precedence:** a provider connected via `opencode auth login`
  may take precedence over `providers.*.settings.apiKey` in config. Verify on
  2.1.2 and, if so, surface "connected via OpenCode" in the UI rather than
  silently overriding.
- **Restart blast radius:** all sessions on the dedicated serve are affected;
  acceptable for a single-user local tool, but document it.
- **Custom providers:** the schema allows `package`/`models`, but arbitrary
  runtime packages may not be installed. v1 UI exposes these as advanced
  fields; ship built-in catalog providers first.
- **No API auth:** `MONITOR_TOKEN` is declared but unimplemented. This feature
  adds a write endpoint; at minimum bind to loopback, add correct CORS, and
  recommend an authenticating proxy for any non-loopback deployment. A bearer
  token remains a separate hardening task.
- **V1→V2 agent keys:** only proceed after a live check; the model/provider
  work does not depend on it.

## 7. File touch list

```
backend/main/internal/settings/{settings.go,settings_test.go}
backend/main/internal/harness/{harness.go,harness_test.go}
backend/main/internal/api/{api.go,api_test.go}
backend/main/cmd/monitord/main.go
frontend/monitor/src/lib/{api.ts,types.ts}
frontend/monitor/src/lib/components/ChatDock.svelte
frontend/monitor/src/routes/+layout.svelte
frontend/monitor/src/routes/settings/{+page.svelte,+page.ts}
frontend/monitor/src/routes/audit/+page.ts        # shared API base
.env.example
README.md
docs/LOCAL_FIRST.md
docs/plans/3-llm-configuration.md                 # this file
```

## 8. Implementation notes (2026-10-24)

- Shipped `internal/settings`, the `--config` flag, `GET/PUT /api/settings/llm`,
  `POST /api/settings/llm/reload`, `GET /api/llm/models`, and `/settings`.
- The harness now rebuilds `OPENCODE_CONFIG_CONTENT` from the store on every
  start, passes `--model` per run, and supports `WithExclusive` save+restart
  without tearing down an active chat.
- Config shape migrated to OpenCode V2 (`agents`, `permissions`, `shell`);
  verified against OpenCode 2.1.2.
- `/api/provider` returned an empty list in some project locations while
  `/api/model` was populated. The catalog now treats models as the source of
  truth and enriches them with provider metadata when available.
- `model` is stored as the full OpenCode reference (`provider/model`, optional
  `#variant`); slashes inside a model ID (e.g. `openrouter/anthropic/...`) are
  supported.
- CORS now defaults to same-origin (still overridable with
  `MONITOR_CORS_ORIGIN=*`); `PUT` was added to the preflight methods.
- SSR now reads `MONITORD_URL` from `process.env` at runtime, so monitord can run
  on a non-default port without rebuilding the frontend.
- UI: separate model-provider and credential-provider selectors so adding a key
  does not silently change the model; the chat dock shows the active model.
- Live checks: model switch persisted across reload, credential save restarted
  the agent in ~0.3 s, remove-provider cleaned the file, chat header updated,
  no browser console errors.
- Not built: the optional connection-test endpoint and the gated mock-provider
  E2E test. `ErrChatInFlight`/409 is covered by the harness sentinel test.

## 9. Follow-up: integration catalog (2026-10-24)

- `/api/provider` is unreliable in OpenCode 2.1.4 (returns `[]` when
  `OPENCODE_CONFIG_CONTENT` is set) and `/api/model` only lists providers that
  are already authenticated. The catalog now also calls `/api/integration`,
  which returns every known provider plus its active `credential`/`env`
  connections.
- Unconnected providers (Anthropic, OpenAI, Google, …) are included with an
  empty model list, so they can be selected in **Provider credentials** and
  activated with a key.
- Credential state now distinguishes Monitor (`file`), environment (`env`), and
  OpenCode-managed (`opencode`) credentials. Providers connected through
  `opencode auth login` no longer show "no key configured".
- Catalog ordering: connected providers with models first, then other connected
  providers, then curated popular providers, then providers with models, then
  the rest. The credentials picker has a text filter because the integration
  catalog is large.
- The model-provider picker only lists connected providers or providers with
  discovered models; add the key first in **Provider credentials**.
- `GET /api/llm/models` now returns the merged integration catalog; existing
  fields are unchanged.
