# opencode-enhancer

CLIProxyAPI native plugin that makes **OpenCode Go** (`opencode.ai/zen/go/v1`)
and **OpenCode Zen** (`opencode.ai/zen/v1`) work reliably through CLIProxyAPI.

OpenCode monitors upstream traffic and requires every request to:

1. carry a **stable `x-opencode-session`** per conversation (sticky routing +
   prompt-cache affinity; requests missing it may error since 09/06),
2. identify itself with a **real agent User-Agent** (not a generic SDK /
   proxy name like `cli-proxy-openai-compat`),
3. send **typical coding-agent traffic** (zen free tier rejects Codex
   `additional_tools` input entries with HTTP 400).

CLIProxyAPI's built-in executors drop or mangle these signals. This plugin
restores them on every request routed to an OpenCode upstream.

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
| **Free-tier fingerprint** | `request.intercept_after` | Sends the complete official-client fingerprint the Zen free tier gates on: `User-Agent: opencode/1.18.31`, `X-Opencode-Client: desktop`, `X-Opencode-Project: global`, a `ses_…`-shaped session, a fresh `msg_…` request id, `Accept: text/event-stream`, forced `stream: true`, and the `bash/glob/grep/read` tool quartet. On the Responses path it also sets `store: false` and drops prior-turn `reasoning` / `encrypted_content`. Applies to free-tier models only; paid builds are untouched. |
| Session injection | `request.intercept_after` | Resolves a stable session id from the client's own session headers (Codex `Session-Id`/`Thread-Id`, Claude Code `X-Claude-Code-Session-Id`, DeepSeek Harness, OpenCode native, CPA `canonical_session_id`, body-content hash fallback) and injects it as `x-opencode-session`. Derived values are SHA-256 hashed before leaving the proxy; a native OpenCode session is never overridden. |
| Client identity | `request.intercept_after` | Injects `X-Opencode-Client` when the client identified itself (`codex`, `claude-code`, `opencode`). **Unidentified clients get no identity header at all** — omitting beats sending a self-identifying proxy label. |
| User-Agent rewrite | `request.intercept_after` | **Dynamic by default**: forwards the client's own User-Agent (real name + real version, never stale). Generic SDK/HTTP-library UAs (`Go-http-client`, `curl/`, `axios`, `OpenAI/Python`, …) are replaced with a **neutral** agent UA (`coding-agent/1.0`, configurable) that carries no proxy marker. Modes: `passthrough` (default), `static`, `map`, `template`. |
| Zen free-tier body cleanup | `request.intercept_after` | Strips `input[]` entries of type `additional_tools` for free-tier models only. Free-tier detection is **dynamic**: any model whose final segment ends with `-free` or `:free` (configurable markers), plus explicit allow/deny lists. Paid builds (`muse-spark-1.3-contributor`) are never touched. |
| Target detection | `scheduler.pick` + intercept | Marks auths whose provider base URL contains a marker (default `opencode.ai`), plus optional auth-prefix and model-glob matching. |

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

### 3. Enable it in `config.yaml`

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    opencode-enhancer:
      enabled: true
      priority: 100
```

Restart CLIProxyAPI and confirm:

```bash
docker restart cli-proxy-api        # or restart the binary
docker logs cli-proxy-api | grep opencode-enhancer
# pluginhost: plugin registered plugin_id=opencode-enhancer ...
```

### 4. Add the credential glue (required)

The plugin injects headers into the *execution headers*. Built-in executors
only put headers on the wire that are declared in the credential's `headers:`
map, so add `$` references on **every OpenCode credential**:

`X-Opencode-Project` and `X-Opencode-Request` are part of the free-tier
fingerprint, so they must be declared too — a header the credential does not
declare never reaches the wire, and a fingerprint missing one part is a 403.

```yaml
# OpenAI-compatible provider (chat completions models)
openai-compatibility:
  - name: "opencode-go"
    base-url: "https://opencode.ai/zen/go/v1"
    headers:
      User-Agent: "$X-Opencode-User-Agent"
      X-Opencode-Session: "$X-Opencode-Session"
      X-Opencode-Client: "$X-Opencode-Client"
      X-Opencode-Project: "$X-Opencode-Project"
      X-Opencode-Request: "$X-Opencode-Request"
      Accept: "$Accept"
    api-key-entries:
      # Free tier authenticates with the pooled public key, not a personal one.
      - api-key: "public"
    models:
      - name: "mimo-v2.5-free"
        alias: "mimo-v2.5-free"
      # ... add the models you use

# Paid credential — same provider, its own entry, real key, no fingerprint.
  - name: "opencode-go-paid"
    base-url: "https://opencode.ai/zen/go/v1"
    headers:
      User-Agent: "$X-Opencode-User-Agent"
      X-Opencode-Session: "$X-Opencode-Session"
      X-Opencode-Client: "$X-Opencode-Client"
    api-key-entries:
      - api-key: "${OPENCODE_GO_API_KEY}"
    models:
      - name: "glm-5.3"
        alias: "glm-5.3"

# Codex-style provider (responses models, e.g. muse)
codex-api-key:
  - api-key: "public"
    base-url: "https://opencode.ai/zen/v1"
    headers:
      X-Opencode-Session: "$X-Opencode-Session"
      X-Opencode-Client: "$X-Opencode-Client"
      X-Opencode-Project: "$X-Opencode-Project"
      X-Opencode-Request: "$X-Opencode-Request"
      Accept: "$Accept"
    models:
      - name: "muse-spark-1.3-contributor-free"
        alias: "muse-free"
```

> [!WARNING]
> **Free-tier muse on the codex path is not fixed yet.** The codex executor
> applies its own `codex-tui/…` device-profile User-Agent *after* custom
> headers, so the plugin's `opencode/1.18.31` never reaches the wire and the
> free-tier gate still fails. Free-tier **chat** models on the
> `openai-compatibility` path are unaffected. See
> [Known limitations](#known-limitations).

`$Name` copies the value from the (plugin-augmented) execution headers; when
absent the header is omitted. On the codex path the host sends its own
`codex-tui/...` User-Agent, which is already a validated agent UA, so no UA
glue is needed there.

## Configuration reference

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
        fallback_to_request_id: false       # per-request id: NOT sticky, off by default

      user_agent:
        rewrite: true
        mode: "passthrough"                 # passthrough | static | map | template
        value: ""                           # static mode: fixed UA for all requests
        template: ""                        # template mode: "{name}/{version} (via cliproxy)"
        fallback_value: "coding-agent/1.0"  # UA when the client UA is generic/empty
        custom_ua:                          # map mode / per-client override
          codex: "codex-cli/1.0"
          default: "coding-agent/1.0"
        generic_patterns: []                # extra UA substrings treated as generic
        client_map:
          codex: "codex"
          claude: "claude-code"
          opencode: "opencode"
          generic: ""                       # empty = omit X-Opencode-Client header
        outbound_header: "X-Opencode-User-Agent"  # header the glue maps to User-Agent
        set_user_agent_header: false              # also set User-Agent directly

      fingerprint:
        enabled: true                     # send the official client fingerprint
        user_agent: "opencode/1.18.31"    # must be opencode/>=1.17
        client: "desktop"                 # X-Opencode-Client
        project: "global"                 # X-Opencode-Project
        accept: "text/event-stream"       # "" to leave Accept alone
        force_stream: true                # stream:false is a 403 gate
        inject_tools: ["bash", "glob", "grep", "read"]
        strip_reasoning: true             # Responses: drop prior reasoning items
        free_only: true                   # paid builds are never reshaped

      body_cleanup:
        strip_additional_tools: true
        free_markers: ["-free", ":free"]    # dynamic free-tier detection
        zen_free_models: []                 # extra exact names treated as free
        zen_paid_models: []                 # exact names never stripped (guard)
        strip_types: ["additional_tools"]   # input[] entry types to drop

      target:
        base_url_markers: ["opencode.ai"]   # auto-match auths by provider URL
        auth_prefixes: ["openai-compatibility:opencode:"]
        models: []                          # optional model globs (e.g. "muse-*")

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
5. Request id — **off by default** (`fallback_to_request_id: false`). A request
   id is not a conversation id: forwarding it gives upstream a new session on
   every request and destroys prompt-cache affinity. Enable it only to escape a
   hard `MissingSessionID` 400 when the body could not be hashed either.

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

Tested end-to-end against CLIProxyAPI v7.2.157 with a mock OpenCode upstream,
then live against a production CLIProxyAPI (v7.2.157, systemd) with real
OpenCode Zen / Go credentials:

- `registered: true`, `effective_enabled: true` in `/v0/management/plugins`.
- OpenAI-compat path: wire request carries `User-Agent: codex-tui/0.153.3`
  (overrides the hardcoded `cli-proxy-openai-compat`), `X-Opencode-Session:
  <sha256(client session)>`, `X-Opencode-Client: codex`.
- Codex path: `additional_tools` stripped from `input[]` for `muse-free`;
  preserved for `muse-spark-1.3-contributor`.
- Target matching works with **only** `base_url_markers` set (scheduler hook
  marks auths by provider URL; no prefix/model config needed).
- Live, per-request observability via `host.log` (opt-in, `logging.enabled:
  true`; **off by default**): every shaped request logs `session_source`,
  `session`, `client_type`, `client_ua`, `outbound_ua`, `auth`, `body_cleanup`
  to the CPA log file.

### Live log example

```
opencode-enhancer: shaped auth=openai-compatibility:opencode zen:... body_cleanup=false
  client_type=codex client_ua=codex-tui/0.153.3 outbound_ua=codex-tui/0.153.3
  session=895dd736... session_source=header model=mimo-v2.5-free
opencode-enhancer: shaped auth=... body_cleanup=false client_type=generic
  client_ua=python-requests/2.31.0 outbound_ua=opencode-cliproxy/0.2.0
  session=5fa71b73... session_source=metadata model=mimo-v2.5-free
opencode-enhancer: shaped auth=... client_type=opencode client_ua=opencode/1.2.3
  outbound_ua=opencode/1.2.3 session=native (preserved) session_source=native
```

`session_source` values: `header` (client session header), `metadata` (CPA
canonical session id), `body` (first-user-turn hash), `native` (client's own
`x-opencode-session`, preserved verbatim).

## Known limitations

- **Free-tier muse (codex path) still 403s.** The codex executor stamps its own
  device-profile `codex-tui/…` User-Agent *after* custom headers, so the
  plugin's `opencode/1.18.31` never reaches the wire. The free tier rejects
  any non-`opencode/>=1.17` UA, so muse free models keep failing there until
  CLIProxyAPI lets a plugin win that header. Free-tier **chat** models on the
  `openai-compatibility` path are unaffected. Paid muse is unaffected: it is
  never fingerprinted.
- **`force_stream: true` breaks non-streaming clients.** The free tier rejects
  `stream: false` outright, so the plugin flips it; a client that asked for a
  non-streamed response then gets SSE the executor does not expect. Those
  requests would have 403'd anyway. Set `fingerprint.force_stream: false` if
  you would rather keep the client's own choice and lose the free tier.
- **The fingerprint is a moving target.** `opencode/1.18.31`, the tool quartet,
  and the `ses_`/`msg_` id shapes track a specific client release. When
  upstream changes its gates, update `fingerprint.user_agent` /
  `fingerprint.inject_tools` in config — no rebuild needed.
- **Not verified against live upstream in this revision.** The gates above are
  reproduced from the official client's behaviour and cross-checked against a
  working independent implementation; the plugin's own coverage is unit tests,
  not a live 200 from Zen.

## Host quirks discovered

- `request.intercept_before` **replaces** the whole header set when a
  non-empty `Headers` map is returned (`finalInterceptorHeaders`), unlike the
  documented merge semantics. This plugin therefore does all work in
  `request.intercept_after`, which merges.
- On the codex path the executor applies its own device-profile `User-Agent`
  after custom headers, so UA glue is ineffective there — but the host's
  `codex-tui/...` UA is already a validated agent UA, so this is fine.
- Config-only alternative for the session header: `X-Opencode-Session:
  "$CPA-SESSION-ID"` in the credential `headers:` map works without any
  plugin when the client sends a recognizable session header, but lacks the
  body-hash fallback, per-client UA mapping, and zen body cleanup.

## Development

```bash
go test ./...        # unit tests
go vet ./...         # vet
./build.sh           # build the shared library
```

## License

MIT