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
free-tier values; this is a complete, working configuration:

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

## Breaking changes in 0.5.0

Config keys removed or renamed. Nothing below had a demonstrated effect, and
all of it is optional, so a config that sets none of it needs no migration.

| Was | Now |
| --- | --- |
| `body_cleanup:` | `free_tier:` — it no longer cleans anything, it only classifies free vs paid |
| `body_cleanup.free_markers` | `free_tier.markers` |
| `body_cleanup.zen_free_models` / `zen_paid_models` | `free_tier.free_models` / `paid_models` |
| `body_cleanup.strip_additional_tools` / `strip_types` | removed — it only fired on the codex path, which rejects every free-tier request anyway |
| `user_agent.mode` / `value` / `template` / `custom_ua` / `generic_patterns` | removed — the fingerprint path ignores them, and the paid path is proven to need no UA shaping |
| `session.fallback_to_request_id` | removed — a request id is not a conversation id; the fingerprint path has its own inline fallback |

The `scheduler` capability is gone too: it marked auths by a `base_url` the
host never puts in that metadata, and targeting works by auth-id substring.
The log field `body_cleanup=` is now `body_shaped=`, which is what it reports.

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
        warn_missing_glue: true           # log required credential headers: once per auth
        free_only: true                   # paid builds are never reshaped

      free_tier:                          # classifies free vs paid; gates everything
        markers: ["-free", ":free"]
        free_models: []                     # exact names to treat as free
        paid_models: []                     # exact names never reshaped (guard)

      target:
        base_url_markers: ["opencode.ai"]   # auto-match auths by provider URL
        auth_prefixes: ["opencode"]         # substring of the host's auth id
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
  `session`, `client_type`, `client_ua`, `outbound_ua`, `auth`, `body_shaped`
  to the CPA log file.

### Live log example

```
opencode-enhancer: shaped auth=openai-compatibility:opencode zen:... body_shaped=false
  client_type=codex client_ua=codex-tui/0.153.3 outbound_ua=codex-tui/0.153.3
  session=895dd736... session_source=header model=mimo-v2.5-free
opencode-enhancer: shaped auth=... body_shaped=false client_type=generic
  client_ua=python-requests/2.31.0 outbound_ua=opencode-cliproxy/0.2.0
  session=5fa71b73... session_source=metadata model=mimo-v2.5-free
opencode-enhancer: shaped auth=... client_type=opencode client_ua=opencode/1.2.3
  outbound_ua=opencode/1.2.3 session=native (preserved) session_source=native
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

CLIProxyAPI derives that id from the credential's display name. Observed forms
on one live host: `openai-compatible-opencode zen` in error messages and
`openai-compatibility:opencode zen:d4fc8bb50803` in the interceptor metadata. The
default `auth_prefixes: ["opencode"]` matches it as a substring; add your own
marker if you named the credential something without "opencode" in it.

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

Two corrections to earlier notes in this README, both wrong:

- `finalInterceptorHeaders` does **not** govern upstream request headers. It
  is used in `handlers_stream.go` for the **response** headers a stream
  interceptor returns downstream. `intercept_before` offers no way around the
  glue, and this plugin's choice of `intercept_after` rests on the merge
  semantics above, not on that function.
- Plugin-supplied auth metadata is not a way in either: `AuthRefreshResponse`
  can carry `headers` metadata, but that hook belongs to the plugin that owns
  the credential's provider, and a built-in `openai-compatibility` API-key
  credential has no refresh cycle to hook.

## Other host quirks

- On the codex path the executor applies its own device-profile `User-Agent`
  after custom headers, so UA glue is ineffective there. Confirmed against
  live Zen: `codex-tui/0.153.3` is rejected with 403 even when every other
  gate is satisfied, which is why free-tier muse cannot work on that path.
- Config-only alternative for the session header: `X-Opencode-Session:
  "$CPA-SESSION-ID"` in the credential `headers:` map works without any
  plugin when the client sends a recognizable session header, but lacks the
  body-hash fallback, per-client UA mapping, and zen body cleanup. It does
  not produce the `ses_`-shaped id the free tier requires.

## Development

```bash
go test ./...        # unit tests
go vet ./...         # vet
./build.sh           # build the shared library
```

## License

MIT