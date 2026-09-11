package plugin

import (
	"net/http"
	"strings"
)

// Client type identifiers used for UA / identity selection.
const (
	ClientCodex    = "codex"
	ClientClaude   = "claude"
	ClientOpenCode = "opencode"
	ClientGeneric  = "generic"
)

// User-Agent rewrite modes.
const (
	// UAModePassthrough forwards the client's own User-Agent upstream. The
	// client's UA carries its real name AND version, so it never goes stale.
	UAModePassthrough = "passthrough"
	// UAModeStatic sends a single fixed UA for every request.
	UAModeStatic = "static"
	// UAModeMap picks the UA from custom_ua[clientType] (the old behavior).
	UAModeMap = "map"
	// UAModeTemplate builds the UA from a template with {name}, {version},
	// {client} placeholders parsed from the client's own UA.
	UAModeTemplate = "template"
)

// DefaultUAMode is the rewrite mode when user_agent.mode is unset.
const DefaultUAMode = UAModePassthrough

// DefaultFallbackUA is used when the client UA is generic/empty and no
// custom_ua entry matches. It is deliberately neutral: no "proxy"/"cliproxy"
// marker that would reveal the traffic is being reshaped. Override with
// user_agent.fallback_value or custom_ua.default.
const DefaultFallbackUA = "coding-agent/1.0"

// DefaultIdentityHeader is the header carrying the proxy/client identity.
const DefaultIdentityHeader = "X-Opencode-Client"

// DefaultOutboundUAHeader carries the user-agent to be applied on the wire
// by a credential "headers:" entry with a "$" reference.
const DefaultOutboundUAHeader = "X-Opencode-User-Agent"

// defaultGenericUAPatterns match User-Agents that identify an SDK or HTTP
// library rather than a coding agent. OpenCode explicitly rejects generic
// SDK / HTTP-library UAs, so those trigger the fallback UA instead.
var defaultGenericUAPatterns = []string{
	"go-http-client",
	"python-requests", "python-urllib", "aiohttp", "httpx", "httptools",
	"curl/", "wget/", "httpie",
	"okhttp", "axios", "node-fetch", "undici", "got/", "ky/",
	"java/", "libwww", "ruby", "php/", "reqwest", "dart",
	"postmanruntime", "insomnia", "httpclient", "apache-httpclient",
	"guzzle", "faraday", "restsharp", "openai-python", "anthropic-python",
	"openai-go", "anthropic-sdk", "openai/", "anthropic/",
}

// detectClientType classifies the downstream client from its headers.
// Session headers are the most reliable signal (they are client-specific);
// the User-Agent is used as a secondary hint.
func detectClientType(headers http.Header) string {
	switch {
	case hasHeader(headers, "X-Opencode-Session"),
		hasHeader(headers, "X-Session-Affinity"):
		return ClientOpenCode
	case hasHeader(headers, "X-Claude-Code-Session-Id"),
		hasHeader(headers, "X-Claude-Code-Agent-Id"):
		return ClientClaude
	case hasHeader(headers, "Session-Id"),
		hasHeader(headers, "Session_id"),
		hasHeader(headers, "Thread-Id"),
		hasHeader(headers, "Thread_id"),
		hasHeader(headers, "X-Codex-Turn-Metadata"):
		return ClientCodex
	}

	ua := strings.ToLower(headerGet(headers, "User-Agent"))
	switch {
	case strings.Contains(ua, "opencode"):
		return ClientOpenCode
	case strings.Contains(ua, "claude"):
		return ClientClaude
	case strings.Contains(ua, "codex"):
		return ClientCodex
	}
	return ClientGeneric
}

// resolveUserAgent picks the outbound user-agent.
//
//   - An explicit user_agent.value always wins (static).
//   - mode=passthrough (default): forward the client's own User-Agent, unless
//     it is generic/empty, in which case fall back to custom_ua / the proxy
//     identity UA. This keeps real client versions current forever.
//   - mode=map: use custom_ua[clientType] / custom_ua["default"].
//   - mode=template: fill {name} {version} {client} from the client UA.
func resolveUserAgent(cfg Config, clientType, clientUA string) string {
	if v := strings.TrimSpace(cfg.UserAgent.Value); v != "" {
		return v
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.UserAgent.Mode))
	if mode == "" {
		mode = DefaultUAMode
	}
	switch mode {
	case UAModeMap:
		return uaFromMap(cfg, clientType)
	case UAModeTemplate:
		if tmpl := strings.TrimSpace(cfg.UserAgent.Template); tmpl != "" {
			name, version := parseUserAgent(clientUA)
			if name != "" {
				return applyUATemplate(tmpl, name, version, clientType)
			}
		}
		return uaFromMap(cfg, clientType)
	default: // passthrough
		if ua := strings.TrimSpace(clientUA); ua != "" && !isGenericUserAgent(ua, cfg.UserAgent.GenericPatterns) {
			return ua
		}
		return uaFromMap(cfg, clientType)
	}
}

// uaFromMap resolves the UA from custom_ua, then the configured fallback.
// The fallback is a neutral agent-style UA (no proxy marker) so anonymous or
// SDK clients do not reveal that the request was reshaped.
func uaFromMap(cfg Config, clientType string) string {
	if v, ok := cfg.UserAgent.CustomUA[clientType]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := cfg.UserAgent.CustomUA["default"]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v := strings.TrimSpace(cfg.UserAgent.FallbackValue); v != "" {
		return v
	}
	return DefaultFallbackUA
}

// isGenericUserAgent reports whether a User-Agent identifies an SDK / HTTP
// library (or is too short to be meaningful) rather than a real agent.
func isGenericUserAgent(ua string, extraPatterns []string) bool {
	lower := strings.ToLower(strings.TrimSpace(ua))
	if lower == "" || len(lower) < 3 {
		return true
	}
	patterns := defaultGenericUAPatterns
	if len(extraPatterns) > 0 {
		patterns = append(patterns, extraPatterns...)
	}
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" && strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// parseUserAgent extracts a name/version pair from a client User-Agent.
// "codex-tui/0.153.3 (Mac OS ...)" → ("codex-tui", "0.153.3").
func parseUserAgent(ua string) (name, version string) {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return "", ""
	}
	first := ua
	if i := strings.IndexAny(ua, " (["); i >= 0 {
		first = ua[:i]
	}
	first = strings.TrimSpace(first)
	if i := strings.Index(first, "/"); i >= 0 {
		return strings.TrimSpace(first[:i]), strings.TrimSpace(first[i+1:])
	}
	return first, ""
}

// applyUATemplate fills {name}, {version}, {client} placeholders.
// Unknown placeholders are left untouched.
func applyUATemplate(tmpl, name, version, clientType string) string {
	out := tmpl
	out = strings.ReplaceAll(out, "{name}", name)
	out = strings.ReplaceAll(out, "{version}", version)
	out = strings.ReplaceAll(out, "{client}", clientType)
	return strings.TrimSpace(out)
}

// resolveClientIdentity picks the identity value sent as X-Opencode-Client.
// An explicitly empty map value (or no entry) means "omit the header": for
// unidentified clients, sending nothing beats sending a self-identifying
// proxy label.
func resolveClientIdentity(cfg Config, clientType string) string {
	if v, ok := cfg.UserAgent.ClientMap[clientType]; ok {
		return strings.TrimSpace(v)
	}
	if clientType != "" && clientType != ClientGeneric {
		return clientType
	}
	return ""
}

// headerGet returns the first value for name (case-insensitive).
func headerGet(headers http.Header, name string) string {
	v, _ := headerValue(headers, name)
	return v
}
