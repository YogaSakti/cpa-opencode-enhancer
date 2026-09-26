# opencode-enhancer

CLIProxyAPI native plugin that makes the **OpenCode free tier** usable through
CLIProxyAPI, for chat models (`openai-compatibility` credentials) and for the
Responses-only Muse models (`codex-api-key` credentials).

OpenCode gates its free tier on the complete official-client fingerprint. A
request missing any one part is rejected with:

```
403 {"type":"error","error":{"type":"FreeTierError",
     "message":"OpenCode's free tier can only be used from within OpenCode"}}
```

Paid models need none of this and are never reshaped: a paid model answers 200
to a bare `curl` with `stream:false` and no OpenCode headers. The one exception
is `zen/go/v1`, which returns `400 MissingSessionID` without
`x-opencode-session`; the plugin supplies it.

> [!IMPORTANT]
> **Personal project — no affiliation, no warranty, takedown on request.**
>
> This plugin was written for the author's own private use and is published
> as-is. Read [Disclaimer](#disclaimer) before using it.

## Disclaimer

**No affiliation.** This project is an independent, personal utility. It is
**not affiliated with, endorsed by, sponsored by, or connected to** OpenCode,
OpenCode Zen, OpenCode Go, CLIProxyAPI, or any of their maintainers, vendors,
or affiliates. All product names, trademarks, and logos are the property of
their respective owners and are used here only for descriptive, nominative
purposes.

**Personal use only.** It was built to solve the author's own setup and is
shared in case it is useful to others. It is not a product, not a service, and
comes with no support commitment, SLA, or roadmap.

**No warranty, no liability.** The software is provided "as is", without
warranty of any kind, express or implied, including but not limited to the
warranties of merchantability, fitness for a particular purpose, and
non-infringement. To the maximum extent permitted by law, the author accepts
**no responsibility or liability** for any damages, losses, account
suspensions, service terminations, quota changes, billing consequences, data
loss, or other harm arising from or connected to the use, misuse, or inability
to use this software. **You run it entirely at your own risk.** You are
responsible for complying with the terms of service of every service you route
traffic through, including OpenCode Zen / Go and CLIProxyAPI.

**Takedown / removal on request.** If you are OpenCode, CLIProxyAPI, or an
authorized representative of either, and you want this repository (or any
release artifact) taken down, features removed, or behaviour changed — just
ask. Open an issue or contact the maintainer and it will be **removed or
adjusted promptly**, no argument, no counter-claim, no pushback.

**Cooperation.** Nothing here is intended to circumvent, defeat, or evade any
provider's systems, limits, or protections, and it is not intended to enable
abuse. If a maintainer of an affected service asks for a change, removal, or
restriction, note that the author is happy to comply immediately.

By using this software you acknowledge that you have read, understood, and
accepted this disclaimer.

## What it does

| Feature | Hook | Behavior |
| --- | --- | --- |
| **Free-tier fingerprint** | `request.intercept_after` | Sends the complete official-client fingerprint the Zen free tier gates on: `User-Agent: opencode/1.18.31`, `X-Opencode-Client: desktop`, `X-Opencode-Project: global`, a `ses_…`-shaped session, a fresh `msg_…` request id, `Accept: text/event-stream`, forced `stream: true`, and the `bash/glob/grep/read` tool quartet, declared in the client's own protocol (OpenAI Chat, Responses, Anthropic Messages or Gemini) so CLIProxyAPI's translators carry it upstream. On the Responses path it also sets `store: false`, drops prior-turn `reasoning` / `encrypted_content`, and removes Codex `additional_tools` input items (see [Known limitations](#known-limitations)). Applies to free-tier models only; paid builds are untouched. |
| Session injection | `request.intercept_after` | Resolves a stable session id from the client's own session headers (Codex `Session-Id`/`Thread-Id`, Claude Code `X-Claude-Code-Session-Id`, DeepSeek Harness, OpenCode native, CPA `canonical_session_id`, body-content hash fallback) and injects it as `x-opencode-session`. Derived values are SHA-256 hashed before leaving the proxy; a native OpenCode session is never overridden. |
| Client identity | `request.intercept_after` | Injects `X-Opencode-Client` when the client identified itself (`codex`, `claude-code`, `opencode`). **Unidentified clients get no identity header at all** — omitting beats sending a self-identifying proxy label. |
| User-Agent rewrite | `request.intercept_after` | **Dynamic by default**: forwards the client's own User-Agent (real name + real version, never stale). Generic SDK/HTTP-library UAs (`Go-http-client`, `curl/`, `axios`, `OpenAI/Python`, …) are replaced with a **neutral** agent UA (`coding-agent/1.0`, configurable) that carries no proxy marker. Applies to the paid path only; the fingerprint path overrides it. |
| Target detection | `request.intercept_after` | Matches the host's auth id against `target.auth_prefixes` as a case-insensitive substring (default `opencode`), or the model against `target.models` globs. A `codex-api-key` credential needs a glob; see [Muse](#muse-responses-only). |

## Endpoints

Read from OpenCode's own gateway code ([anomalyco/opencode](https://github.com/anomalyco/opencode)
`packages/console/app/src/routes/zen/` and `lib/inference-proxy.ts`, commit
`696f41b`). The public `/zen` routes are a thin proxy: with an `oc_sk_…` key,
every `/zen` request is forwarded to the inference host, which is where the
free-tier gate (`FreeTierError`) lives.

| Public route | Forwarded to `https://opencode.ai/inference…` | Format |
| --- | --- | --- |
| `POST /zen/v1/chat/completions` | `/openai/v1/chat/completions` | OpenAI Chat |
| `POST /zen/v1/responses` | `/openai/v1/responses` | OpenAI Responses |
| `POST /zen/v1/messages` | `/anthropic/v1/messages` | Anthropic |
| `POST /zen/v1/models/{model}:streamGenerateContent` | `/google/v1beta/models/{model}:…` | Gemini |
| `POST /zen/v1/systemone` | `/systemone/v1/systemone` | Jev (typed decisions, not text) |
| `GET /zen/v1/models` | `/v1/models` | catalog |
| `POST /zen/go/v1/chat/completions`, `/responses` | `/go/openai/v1/…` | Go subscription |
| `POST /zen/go/v1/messages` | `/go/anthropic/v1/messages` | Go subscription |
| `POST /zen/go/v1/systemone` | `/go/systemone/v1/systemone` | Go subscription |
| `GET /zen/go/v1/models`, `/usage` | `/go/v1/models`, `/go/v1/usage` | Go catalog, usage |

A CLIProxyAPI `base-url` can therefore be either the public prefix or its
inference equivalent. Never include `/chat/completions` or `/responses`: the
executor appends them.

| Use | `base-url` |
| --- | --- |
| Chat models | `https://opencode.ai/zen/v1` or `https://opencode.ai/inference/openai/v1` (verified 2026-09-19) |
| Muse | the same two (`zen/v1` verified 2026-09-26) |
| Go subscription | `https://opencode.ai/zen/go/v1` or `https://opencode.ai/inference/go/openai/v1` |

The catalog sits under a different prefix (`/inference/v1/models`), which is
why `/inference/openai/v1/models` is a 404. That is harmless: CLIProxyAPI serves
requests from the credential's own `models:` list.

The official client reads its provider base URLs from
`models.opencode.ai/api.json` (`opencode` → `zen/v1`, `opencode-go` →
`zen/go/v1`) and picks the endpoint per model from its AI SDK package:
`@ai-sdk/openai` → `/responses`, `@ai-sdk/openai-compatible` →
`/chat/completions`, `@ai-sdk/anthropic` → `/messages`. It sends
`x-opencode-project`, `x-opencode-session`, `x-opencode-request`,
`x-opencode-client` and `User-Agent` on every OpenCode request, the same set
this plugin supplies.

Keys look like `oc_sk_…`; the old `sk-…` keys were revoked.

Free models and the endpoint each one answers on, measured live on 2026-09-26
(check `https://opencode.ai/zen/v1/models` for the current list):

| Endpoint | Models |
| --- | --- |
| `/chat/completions` | `big-pickle`, `space-bunny-free`, `mimo-v2.5-free`, `mimo-v2.6-flash-free`, `ling-3.0-flash-fin-free`, `nemotron-3-ultra-free`, `nemotron-3.5-lightning-free` |
| `/responses` | `muse-spark-1.3-contributor-free`, `muse-spark-1.2-contributor-free` (their `/chat/completions` answers 500) |
| not usable through CPA | `jev-1.13-free` — served on `/zen/v1/systemone`, returns typed decisions rather than text |
| retired | `deepseek-v4-flash-free` — `400 Model is unavailable` |

## Install

### 1. Build

The CLIProxyAPI runtime is glibc-based. Build with CGO and a glibc toolchain
(an alpine/musl build will fail to `dlopen`):

```bash
./build.sh                          # linux/amd64 → dist/linux/amd64/opencode-enhancer.so
GOOS=darwin GOARCH=arm64 ./build.sh # macOS
```

### 2. Place the plugin

```text
<cliproxyapi_root>/plugins/linux/amd64/opencode-enhancer.so
```

Releases ship `opencode-enhancer_<version>_linux_amd64.zip` plus
`checksums.txt`, the layout CLIProxyAPI's plugin store installs from. Point the
store at this repository and it downloads, extracts and names the file itself —
nothing to place by hand.

> [!IMPORTANT]
> Installing by hand instead? Name the file
> `opencode-enhancer-v<version>.so`. A store-managed plugin whose filename
> carries no version is **skipped silently** — no error, no log line,
> `registered: false` in `/v0/management/plugins`, and every request passes
> through unshaped.

### 3. Enable it in `config.yaml`

Everything is optional except `enabled`. The defaults are the verified
free-tier values; this is a complete, working configuration for chat models:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    opencode-enhancer:
      enabled: true
```

Restart CLIProxyAPI and confirm:

```bash
journalctl -u cliproxyapi --no-pager -n 40 | grep opencode-enhancer
# pluginhost: plugin loaded plugin_id=opencode-enhancer version=...
```

### 4. Add the credential glue (required)

The plugin injects headers into the *execution headers*. Built-in executors
only put headers on the wire that are declared in the credential's `headers:`
map, so add `$` references on **every free-tier OpenCode credential**. A
header the credential does not declare never reaches the wire, and a
fingerprint missing one part is a 403.

```yaml
openai-compatibility:
  # FREE credential. Keep it free-only: mixing tiers on one credential is what
  # makes model names ambiguous when aliases drop the -free suffix.
  - name: "Opencode Free"
    base-url: "https://opencode.ai/inference/openai/v1"
    headers:
      User-Agent: "$X-Opencode-User-Agent"
      X-Opencode-Session: "$X-Opencode-Session"
      X-Opencode-Client: "$X-Opencode-Client"
      X-Opencode-Project: "$X-Opencode-Project"
      X-Opencode-Request: "$X-Opencode-Request"
      Accept: "$Accept"
    api-key-entries:
      - api-key: "${OPENCODE_API_KEY}"   # oc_sk_…
    models:
      - name: "mimo-v2.5-free"
        alias: ""
      - name: "nemotron-3-ultra-free"
        alias: ""

  # PAID credential. No fingerprint is applied, so it needs no glue beyond the
  # session header that zen/go/v1 demands.
  - name: "Opencode Paid"
    base-url: "https://opencode.ai/zen/go/v1"
    headers:
      X-Opencode-Session: "$X-Opencode-Session"
    api-key-entries:
      - api-key: "${OPENCODE_API_KEY}"   # same key serves both tiers
    models:
      - name: "glm-5.3"
        alias: ""
```

`$Name` copies the value from the (plugin-augmented) execution headers; when
absent the header is omitted.

If you alias free models to drop the `-free` suffix, make sure the alias does
not collide with a paid model of the same name on another credential — the
tier that serves the request then depends on scheduling, and the billing
differs. Give one side a distinct alias.

### Muse (Responses-only)

Muse answers only on `/responses`, so it cannot go on an
`openai-compatibility` credential (that executor posts to
`/chat/completions`). Use a `codex-api-key` credential, whose executor posts to
`<base-url>/responses`, and target it by model in the plugin config:

```yaml
codex-api-key:
  - api-key: "${OPENCODE_API_KEY}"          # oc_sk_…
    base-url: "https://opencode.ai/zen/v1"  # or …/inference/openai/v1; no /responses suffix
    disable-codex-cloaking: true            # this credential only (CLIProxyAPI >= 7.3.17)
    headers:
      User-Agent: "$X-Opencode-User-Agent"
      X-Opencode-Client: "$X-Opencode-Client"
      X-Opencode-Session: "$X-Opencode-Session"
      X-Opencode-Project: "$X-Opencode-Project"
      X-Opencode-Request: "$X-Opencode-Request"
      Accept: "$Accept"
    models:
      - name: "muse-spark-1.3-contributor-free"
        alias: ""
      - name: "muse-spark-1.2-contributor-free"
        alias: ""

plugins:
  configs:
    opencode-enhancer:
      enabled: true
      target:
        models: ["muse-*"]
```

- **`target.models` is required.** CLIProxyAPI passes interceptors no base
  URL, and a `codex-api-key` auth id is `codex:apikey:<hash>`, so
  `auth_prefixes` cannot match it. Without the glob the plugin skips the
  request silently and upstream answers `403 FreeTierError`.
- **`disable-codex-cloaking: true`.** With cloaking on, the Codex executor
  replaces `User-Agent` with its own device profile after custom headers, and
  the free tier rejects it. The per-credential switch arrived after
  CLIProxyAPI 7.3.11; on older hosts only the global
  `codex.disable-codex-cloaking` exists, which also affects Codex OAuth, so run
  Muse in a dedicated instance there.
- Clients may use any protocol (Chat Completions, Responses, Claude);
  CLIProxyAPI translates to Responses.

`muse-spark-*-contributor` (no `-free`) on Go is a subscription model, not free
tier. OpenCode's gateway requires Go training consent on the workspace for it
(`DataPolicyError` otherwise). The plugin does not fingerprint it: earlier
versions tried, but could only recognize an `openai-compatibility` credential,
which cannot serve Muse, and a 2026-09-22 run through a `codex-api-key`
credential on `zen/go/v1` completed without the fingerprint. If Go ever
answers `FreeTierError`, opt the model in with `free_tier.free_models`.

## Configuration reference

Every key below is optional and shown at its default. Set one only to override
the default — the plugin works with none of them.

```yaml
plugins:
  configs:
    opencode-enhancer:
      enabled: true
      priority: 100

      session:
        header_name: "x-opencode-session"   # header injected upstream
        source_headers:                     # client headers checked, in order
          - X-Opencode-Session
          - Session-Id
          - Session_id
          - Thread-Id
          - Thread_id
          - X-Claude-Code-Session-Id
          - X-DeepSeek-Harness-Session-Id
          - X-Session-Affinity
          - X-Session-Id
          - X-Client-Request-Id
        hash_derived: true                  # SHA-256 derived ids before sending
        fallback_to_body_hash: true         # hash first user turn when no header

      user_agent:                         # non-fingerprint (paid) path only
        rewrite: true
        fallback_value: "coding-agent/1.0"  # UA when the client's own is a generic SDK one
        client_map:
          codex: "codex"
          claude: "claude-code"
          opencode: "opencode"
          generic: ""                       # empty = omit X-Opencode-Client
        outbound_header: "X-Opencode-User-Agent"
        set_user_agent_header: false

      fingerprint:
        enabled: true                     # send the official client fingerprint
        user_agent: "opencode/1.18.31"    # must be opencode/>=1.17
        client: "desktop"                 # X-Opencode-Client
        project: "global"                 # X-Opencode-Project
        accept: "text/event-stream"       # "" to leave Accept alone
        force_stream: true                # stream:false is a 403 gate
        inject_tools: ["bash", "glob", "grep", "read"]
        strip_reasoning: true             # Responses: drop prior reasoning items
        strip_additional_tools: true      # Responses upstream: drop Codex additional_tools items
        warn_missing_glue: true           # log required credential headers: once per auth
        free_only: true                   # paid builds are never reshaped

      free_tier:                          # classifies free vs paid; gates everything
        markers: ["-free", ":free"]
        free_models: []                     # exact names to treat as free
        paid_models: []                     # exact names never reshaped (guard)

      target:
        auth_prefixes: ["opencode"]         # substring of the host's auth id
        models: []                          # model globs; codex-api-key needs one (e.g. "muse-*")

      logging:
        enabled: false                      # per-request host.log lines (default OFF)
```

## Session resolution precedence

1. Native `x-opencode-session` (real OpenCode client) — authoritative, never
   rewritten or hashed. A value that cannot be forwarded as a header (CRLF,
   oversized) falls through to the derived sources instead of breaking the
   outbound request.
2. Client session headers in `source_headers` order.
3. CPA `canonical_session_id` metadata (host-computed stable identity).
4. SHA-256 of the first user turn content (stable across turns of the same
   conversation; system prompts excluded).
Derived values (2–3) are hashed when `hash_derived: true` so the client's raw
session id never leaves the proxy.

`source_headers` is ordered by how conversation-specific each header is, not
alphabetically: `X-Session-Affinity` outranks the generic `X-Session-Id` because
only dsh's pi-ai transport sets it and it is conversation-scoped. Two headers are
deliberately **not** in the defaults — `X-Conversation-Id` and `X-Thread-Id` are
proxy/server stamps that may vary per request, and because headers are consulted
before the body-hash fallback an unstable value there would replace a stable
session with a fresh one on every call. Add them back only if your client
genuinely sends stable values.

Request-scoped identities are marked unstable and are never used to derive a
sticky session.

## Verified behavior

**Muse, 2026-09-26**, production CLIProxyAPI 7.3.17 (systemd), real `oc_sk_`
credential, plugin v0.5.2, the [Muse](#muse-responses-only) configuration above:

- Chat Completions client → `muse-spark-1.2-contributor-free`: **200**, `OK`
  (CLIProxyAPI translates to `/responses`).
- Codex Responses payload with `additional_tools` in `input[0]`:
  `response.completed`. The same payload sent straight upstream:
  `400 input[0] did not match any supported type`.
- Without `target.models`: `403 FreeTierError`, and no plugin log line —
  the request was never targeted.
- Straight upstream, Muse accepts top-level `function` and `namespace` tools
  and rejects `custom` ones (`custom tools are not supported on this
  endpoint`).

**Chat models, 2026-09-19**, CLIProxyAPI 7.3.8, plugin v0.5.0:

- Free tier, through CPA, `base-url` `…/inference/openai/v1`, streaming
  client: `mimo-v2.5-free`, `ling-3.0-flash-fin-free`, `nemotron-3-ultra-free`
  and `nemotron-3.5-lightning-free` all return **200** and assemble to `OK`.
- The same models with a bare request (curl UA, `stream:false`, no OpenCode
  headers) return **403 FreeTierError**. The fingerprint is what makes the
  difference, and each gate matters: dropping the tool quartet, or sending
  `codex-tui/0.153.3` instead of `opencode/1.18.31`, restores the 403.
- Paid is untouched and unaffected: `glm-5.3-flash` and `deepseek-v4-pro`
  return 200, and the log shows `fingerprint=false` for them.
- Wire capture of a shaped request: inbound `User-Agent: curl/8.5.0` with
  `"stream":false`, outbound `User-Agent: opencode/1.18.31`,
  `X-Opencode-Client: desktop`, `X-Opencode-Project: global`,
  `X-Opencode-Session: ses_f56b02280823mByoHc9xwFUV68`,
  `X-Opencode-Request: msg_0b86c8beb001HxblIuY7sPnQyp`, body `"stream":true`
  with the `bash/glob/grep/read` tools appended.

Per-request observability via `host.log` is opt-in (`logging.enabled: true`):
each shaped request logs `session_source`, `session`, `client_type`,
`client_ua`, `outbound_ua`, `auth`, `fingerprint` and `body_shaped`. The
missing-glue warning is logged regardless of this flag.

### Live log example

```
opencode-enhancer: shaped auth=openai-compatibility:opencode zen:d4fc8bb50803
  body_shaped=true client_type=generic client_ua=curl/8.5.0 fingerprint=true
  identity=desktop outbound_ua=opencode/1.18.31
  session=ses_f56b02280823mByoHc9xwFUV68 session_source=metadata
  model=nemotron-3-ultra-free
opencode-enhancer: shaped auth=... fingerprint=false client_type=codex
  client_ua=codex-tui/0.153.3 outbound_ua=codex-tui/0.153.3
  session_source=header model=glm-5.3-flash
```

`session_source` values: `header` (client session header), `metadata` (CPA
canonical session id), `body` (first-user-turn hash), `native` (client's own
`x-opencode-session`, preserved verbatim).

## Troubleshooting

Both failures below are silent — the plugin does nothing and every request is
rejected upstream with `403 FreeTierError`.

**1. The plugin never loaded.** Check it registered:

```bash
journalctl -u cliproxyapi --no-pager -n 40 | grep opencode-enhancer
# want: pluginhost: plugin registered plugin_id=opencode-enhancer version=...
```

Nothing at all (not even an error) means CLIProxyAPI skipped the file. The
usual cause is a store-managed plugin whose filename carries no version:
rename it to `opencode-enhancer-v<version>.so` and restart.

CLIProxyAPI hot-reloads a plugin when it detects a **new version** — dropping
in `opencode-enhancer-v0.4.3.so` beside a running `v0.4.2` logs
`plugin hot reloaded active_version=0.4.3 retired_version=0.4.2` with no
restart. A rename that does not change the detected version does not trigger
it, so recovering from the unversioned-filename case does need a restart.

**2. The plugin loaded but skips every request.** Turn on `logging.enabled`
and look for a `shaped` line per request. No line means `target` matched
nothing. Read the auth id out of an error response:

```text
auth_unavailable: no auth available (providers=openai-compatible-opencode zen, model=...)
                                               ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
```

CLIProxyAPI derives an `openai-compatibility` auth id from the credential's
display name (`openai-compatibility:opencode zen:d4fc8bb50803` in the
interceptor metadata). The default `auth_prefixes: ["opencode"]` matches it as
a substring; add your own marker if you named the credential something without
"opencode" in it. A `codex-api-key` auth id (`codex:apikey:<hash>`) carries no
name at all: target it with `target.models`.

## Known limitations

- **Muse requires a Responses credential.** The plugin can shape a Muse
  request but cannot change the executor CLIProxyAPI selects. Configure Muse
  under `codex-api-key`, not `openai-compatibility`; see
  [Muse](#muse-responses-only).
- **Codex `additional_tools` is removed on the Muse free tier.** Codex sends
  `{"type":"additional_tools","tools":[…]}` in `input[]`, and the free tier
  rejects it with `400 input[0] did not match any supported type`. When the
  upstream protocol is Responses (`codex` / `openai-response`), the plugin
  drops those items and promotes plain `function` declarations inside them
  to the top-level `tools` (a top-level tool of the same name wins). `custom`
  (Codex's `exec` sandbox) and `namespace` (MCP) declarations are dropped,
  so those capabilities are unavailable there. Translated targets such as
  Chat Completions keep the items, because CLIProxyAPI's translators convert
  them into native tools. Set `fingerprint.strip_additional_tools: false` to
  disable this. This covers what the separate `muse-tools-stripper` plugin
  does, so you do not need both; if that plugin runs first, it discards the
  function declarations before this one can promote them.
- **`force_stream: true` breaks non-streaming clients.** The free tier rejects
  `stream: false` outright, so the plugin flips it; a client that asked for a
  non-streamed response then gets SSE the executor does not expect. Those
  requests would have 403'd anyway. Set `fingerprint.force_stream: false` if
  you would rather keep the client's own choice and lose the free tier.
- **Translated requests keep the client's own stream flag.** When
  CLIProxyAPI translates between protocols (a Claude or Gemini client to an
  OpenAI upstream), the translator takes `stream` from the original request,
  not from the body the plugin returns (`openai_claude_request.go`). A
  non-streaming Claude or Gemini client therefore still gets `403
  FreeTierError`. Claude Code always streams; Gemini clients must use
  `:streamGenerateContent`.
- **The fingerprint is a moving target.** `opencode/1.18.31`, the tool quartet,
  and the `ses_`/`msg_` id shapes track a specific client release. When
  upstream changes its gates, update `fingerprint.user_agent` /
  `fingerprint.inject_tools` in config — no rebuild needed.

## Why the credential glue cannot be dropped

No plugin hook can put a header on the wire. Read against CLIProxyAPI v7.3.8,
the request path is:

1. `request.intercept_before` / `request.intercept_after` return `Headers`,
   which `mergeRequestInterceptorHeaders` merges into `opts.Headers`
   (`sdk/api/handlers/handlers_interceptors.go`).
2. The executor calls
   `util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)`
   (`internal/runtime/executor/openai_compat_executor.go`).
3. `util.extractCustomHeaders` iterates **`attrs`** — the credential's own
   `header:` entries — and consults `opts.Headers` *only* to resolve a `$Name`
   reference. An execution header the credential never declares is never
   emitted.

The outbound request is also built from scratch (the executor sets
`Content-Type`, `Authorization` and `User-Agent` itself); client headers are
not forwarded wholesale. So a header reaches upstream only when the credential
declares it, by design: the plugin proposes a value, the operator decides
which headers may leave.

Because a missing declaration is otherwise invisible — upstream just answers
403 — the plugin logs the exact list of required `headers:` entries once per
credential the first time it fingerprints a request. That line bypasses
`logging.enabled`; silence it with `fingerprint.warn_missing_glue: false`.

## Other host quirks

- Interceptor metadata carries only `selected_auth_id` and
  `selected_auth_index` (a hash); the credential's base URL is never passed.
  Target detection therefore works on auth ids and model names only.
- CLIProxyAPI hot-reloads `config.yaml` through a watch on the file itself.
  An edit that replaces the file (`sed -i`, editors that write a temp file and
  rename it) drops the watch: later edits are ignored, with no log line, until
  the service restarts. Edit the file in place, and confirm a
  `config file changed, reloading` line after each change.

## Development

```bash
go test ./...        # unit tests
go vet ./...         # vet
./build.sh           # build the shared library
```

## License

MIT
