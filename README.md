# opencode-enhancer

CLIProxyAPI native plugin that makes the **OpenCode free tier** usable through
CLIProxyAPI. Free chat models use the plugin's own Go/uTLS executor and native
`opencode-enhancer` credentials. Existing `openai-compatibility` and
`codex-api-key` credentials retain the interceptor behavior described below.

OpenCode checks the client request before serving free-tier models. In a live
test, an invalid session ID returned:

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
| Native free chat transport | `auth_provider`, `model_provider`, `executor` | Loads `opencode-enhancer` auth JSON, registers selected models and aliases, and sends requests directly to OpenCode with Go/uTLS. CPA's registry selects providers, so shared model IDs retain normal priority, rotation, and cooldown behavior. Requires no CPA patch or relay. |
| **Free-tier fingerprint** | `request.intercept_after` | Sends the OpenCode client fingerprint: `User-Agent: opencode/latest/2.0.28/cli`, `X-Opencode-Client: cli`, `X-Opencode-Project: global`, the same timestamped `ses_…` session in `x-opencode-session` and `x-opencode-session-id`, a fresh `msg_…` request id, `Accept: text/event-stream`, forced `stream: true`, and the `bash/shell/glob/grep/read` tools with nonempty parameter schemas. Tools are declared in the client's own protocol (OpenAI Chat, Responses, Anthropic Messages or Gemini) so CLIProxyAPI's translators carry them upstream. On the Responses path it also sets `store: false`, drops prior-turn `reasoning` / `encrypted_content`, and removes Codex `additional_tools` input items (see [Known limitations](#known-limitations)). Applies to free-tier models only; paid builds are untouched. |
| Session injection | `request.intercept_after` | Resolves a stable session id from the client's own session headers (Codex `Session-Id`/`Thread-Id`, Claude Code `X-Claude-Code-Session-Id`, DeepSeek Harness, OpenCode native, CPA `canonical_session_id`, body-content hash fallback) and injects it as `x-opencode-session`. Derived values use a first-seen timestamp and hashed conversation identity; a native OpenCode session is never overridden. |
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

For free chat models, use `/inference/v1`. Re-measured on 2026-10-15, `/zen/v1`
answers the same requests identically (`200` with the fingerprint, `403
FreeTierError` without), so either base URL works as long as the fingerprint
reaches the wire. Never include `/chat/completions` or `/responses`: the
executor appends them.

| Use | `base-url` |
| --- | --- |
| Free chat models | `https://opencode.ai/inference/v1` (verified 2026-10-15) |
| Muse | `/responses` on the free endpoint; availability also depends on region |
| Go subscription | `https://opencode.ai/zen/go/v1` or `https://opencode.ai/inference/go/openai/v1` |

The free catalog is at `/inference/v1/models`. CLIProxyAPI serves requests from
the credential's own `models:` list.

The official client reads its provider base URLs from
`models.opencode.ai/api.json` (`opencode` → `zen/v1`, `opencode-go` →
`zen/go/v1`) and picks the endpoint per model from its AI SDK package:
`@ai-sdk/openai` → `/responses`, `@ai-sdk/openai-compatible` →
`/chat/completions`, `@ai-sdk/anthropic` → `/messages`. In a captured OpenCode
2.0.28 CLI request it sent `x-opencode-project`, `x-opencode-session`,
`x-opencode-client` and `User-Agent`; the plugin supplies these headers and a
per-request `x-opencode-request` ID.

Keys look like `oc_sk_…`; the old `sk-…` keys were revoked.

Free models and the endpoint each one answers on, measured live on 2026-10-15
(check `https://opencode.ai/inference/v1/models` for the current list):

| Endpoint | Models |
| --- | --- |
| `/chat/completions` | `cat-jam-free`, `mimo-v2.6-flash-free`, `space-bunny-free`, `nemotron-3.5-lightning-free`, `longcat-2.5-preview-free`, `step-5-preview-free` (currently `429 Endpoint is unavailable`) |
| `/responses` | `muse-spark-1.3-contributor-free`, `muse-spark-1.2-contributor-free` (their `/chat/completions` answers `400 ModelProtocolUnsupported`) |
| not usable through CPA | `jev-1.13-free` — served on `/zen/v1/systemone`, returns typed decisions rather than text |
| retired | `big-pickle`, `deepseek-v4-flash-free`, `ling-3.0-flash-fin-free`, `nemotron-3-ultra-free`, `mimo-v2.5-free` — no longer in the catalog; a request answers `403 Model access is disabled` (not a `FreeTierError`, so re-adding the marker will not revive them) |

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

### 4. Add a native free-chat credential

The native executor was tested with the unchanged CLIProxyAPI 8.0.43 binary.
Create `opencode-free.json` directly inside CPA's configured `auth-dir`:

```json
{
  "type": "opencode-enhancer",
  "api_key": "oc_sk_your-opencode-key",
  "base_url": "https://opencode.ai/inference/openai/v1",
  "models": [
    {"name": "mimo-v2.6-flash-free", "alias": "mimo-v2.6-flash"}
  ]
}
```

Restrict the file to the CPA service account (`chmod 600` on Linux). This JSON
contains the actual key; it does not expand shell environment variables.
No `headers` glue is required for this native credential. Optional `label`,
`prefix`, `disabled`, `headers`, and `proxy_url` fields are supported.
Credential proxy settings take precedence over CPA's global proxy; otherwise
the transport uses the process proxy environment.
Direct connections to OpenCode use IPv4; explicitly configured proxies keep
their own network routing. This does not change CPA's other providers.

Use the matching Free Model Sync build to monitor this auth file, fetch the
catalog, test models, and save the selected free set. It preserves the key,
existing aliases, and unrelated JSON fields. When migrating an old free-only
compatibility entry, remove its duplicate free models after validating the
native credential.

Free Model Sync v0.11.0 and later provide an **Alias** field for each native
model. Choose any supported alias; `opencode-free/` is not a required prefix.
Leave the field blank to use the upstream model ID. Shared aliases across
providers use CPA's normal provider selection; a distinct alias selects this
provider explicitly.

The native executor currently uses upstream Chat Completions. Muse models
require upstream Responses and are rejected here. The native executor supports
streaming and assembled non-streaming replies; CPA handles client protocol
translation. OAuth login, token counting, and arbitrary HTTP requests are not
implemented by this provider. Per-request proxy overrides are not exposed by
CPA's plugin executor ABI.

### Existing compatibility credentials: header glue

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
    base-url: "https://opencode.ai/inference/v1"
    headers:
      User-Agent: "$X-Opencode-User-Agent"
      X-Opencode-Session: "$X-Opencode-Session"
      X-Opencode-Session-Id: "$X-Opencode-Session-Id"
      X-Opencode-Client: "$X-Opencode-Client"
      X-Opencode-Project: "$X-Opencode-Project"
      X-Opencode-Request: "$X-Opencode-Request"
      Accept: "$Accept"
    api-key-entries:
      - api-key: "${OPENCODE_API_KEY}"   # oc_sk_…
    models:
      - name: "mimo-v2.6-flash-free"
        alias: ""
      - name: "space-bunny-free"
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
        user_agent: "opencode/latest/2.0.28/cli"
        client: "cli"                     # X-Opencode-Client
        project: "global"                 # X-Opencode-Project
        accept: "text/event-stream"       # "" to leave Accept alone
        force_stream: true                # stream:false is a 403 gate
        inject_tools: ["bash", "shell", "glob", "grep", "read"]
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

**Ablation of the free-tier gate, 2026-10-15**, plugin v0.5.7, direct to
`https://opencode.ai/inference/v1/chat/completions` with `mimo-v2.6-flash-free`
and a real `oc_sk_…` key. Each row changes exactly one thing from a passing
request; the plugin's own `intercept_after` output is what is sent.

| Change | Result |
| --- | --- |
| fingerprint as the plugin emits it | **200** |
| drop `X-Opencode-Session` | 403 |
| drop `User-Agent` | 403 |
| drop `X-Opencode-Session-Id` | 403 |
| `X-Opencode-Session-Id` ≠ `X-Opencode-Session` | 403 |
| `User-Agent` → `curl/8.7.1` | 403 |
| session → a UUID | 403 |
| drop `X-Opencode-Client` / `-Project` / `-Request` | **200** (not checked) |
| drop `Authorization` | 403 |
| body `stream:false` | 403 |
| body without the `bash/glob/grep/read` quartet | 403 |
| `stream:false` *and* no tools | 403 |

So four things must hold together: an `opencode/…` User-Agent, a `ses_`-shaped
session echoed identically in both session headers, `"stream":true`, and the
tool quartet present. `x-opencode-client`/`-project`/`-request` are cosmetic.

**Per-model matrix, 2026-10-15** (same key, same plugin path):

| Model | `/chat/completions` | `/responses` |
| --- | --- | --- |
| `cat-jam-free`, `mimo-v2.6-flash-free`, `space-bunny-free`, `nemotron-3.5-lightning-free`, `longcat-2.5-preview-free` | 200 | 403 |
| `step-5-preview-free` | 429 `Endpoint is unavailable` | 403 |
| `muse-spark-1.2/1.3-contributor-free` | 400 `ModelProtocolUnsupported` | 200 |
| `jev-1.13-free` | 400 `ModelProtocolUnsupported` | 403 |
| `big-pickle` (retired) | 403 `Model access is disabled` | 403 |

Paid stays untouched: `glm-5.3` is not reshaped (`fingerprint=false`, UA
forwarded verbatim) and returns 200 on both `/inference/v1` and `/zen/go/v1`.
Same-conversation determinism holds: two turns carrying the same
`X-Session-Affinity` yield the same `ses_` id.

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

- **Native credentials support upstream Chat Completions.** Muse is rejected
  by the native provider and excluded from its Free Model Sync selection.
- **Legacy Muse requires a Responses credential.** The interceptor can shape a Muse
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
- **Legacy `force_stream: true` breaks non-streaming clients.** The free tier rejects
  `stream: false` outright, so the plugin flips it; a client that asked for a
  non-streamed response then gets SSE the executor does not expect. Those
  requests would have 403'd anyway. Set `fingerprint.force_stream: false` if
  you would rather keep the client's own choice and lose the free tier.
- **Legacy translated requests keep the client's own stream flag.** When
  CLIProxyAPI translates between protocols (a Claude or Gemini client to an
  OpenAI upstream), the translator takes `stream` from the original request,
  not from the body the plugin returns (`openai_claude_request.go`). A
  non-streaming Claude or Gemini client therefore still gets `403
  FreeTierError`. Claude Code always streams; Gemini clients must use
  `:streamGenerateContent`.
- **The fingerprint is a moving target.** The default UA, tools, and session
  ID format track OpenCode 2.0.28. UA and tools can be overridden in config;
  an ID-format change needs a plugin update.

## Why legacy credentials need header glue

An interceptor cannot put a header on the built-in executor's wire by itself.
The native plugin executor sets its own headers and needs no glue. For legacy
credentials, read against CLIProxyAPI v7.3.8, the request path is:

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

## OpenCode transport (2026-10-26)

The free-tier request also needs an environment block containing `Working directory:`.
The plugin preserves the client's existing environment and instructions. If no
environment was supplied, it adds `<env>\nWorking directory: not provided\n</env>`
in the source protocol's system context; it does not send a made-up filesystem path.

On the tested Linux host, the same request completed through Bun while standard
Go TLS returned `429 FreeUsageLimitError`. Replacing only Go's TLS ClientHello
with the captured Bun 1.4.4 profile through uTLS v1.8.2 returned HTTP 200, `OK`,
and `[DONE]`. This error alone therefore does not establish exhausted quota.
Updating the User-Agent or response-header passthrough toggle did not resolve it.

A later controlled test found a separate network-family requirement: the same
Go/uTLS request to the same hostname returned 429 over IPv6 and HTTP 200 with
`OK` and `[DONE]` over IPv4. Standard Go TLS over IPv4 still returned 429 in the
control test. Direct native OpenCode connections therefore use uTLS and IPv4,
without pinning an upstream IP address. Configured proxies retain their own
routing. HTTP header order and TCP coalescing were investigated but were not
needed for the demonstrated fix.

The plugin's native executor owns the OpenCode HTTP request and uses the
measured uTLS profile. Keep the direct OpenCode base URL. Both plugins work
inside the unchanged CPA process, without a loopback relay or Bun service.
Legacy compatibility credentials still use CPA's built-in TLS transport;
adding interceptor headers alone does not switch them to the native executor.

The native plugin was tested on unchanged CPA 8.0.43 on 2026-10-26 with a real
`mimo-v2.6-flash-free` request and an auth file containing no header glue.
Chat Completions returned HTTP 200, text `OK`, and `[DONE]`. Responses, Claude,
and Gemini client streams also returned `OK` with their protocol's completion
event; a non-streaming Chat request returned an assembled JSON completion.

Use the matching patched free-model-sync build for the environment context and
visible-text-plus-completion probe checks. A 128-token chat probe gives reasoning
models room to produce visible output; a 200 stream containing only reasoning
does not pass. The TLS profile follows the measured Bun version and may need
refreshing if upstream client requirements change.

## Development

```bash
go test ./...        # unit tests
go vet ./...         # vet
./build.sh           # build the shared library
```

## License

MIT
