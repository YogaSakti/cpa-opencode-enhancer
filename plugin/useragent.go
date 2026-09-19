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

// resolveUserAgent picks the outbound user-agent for a NON-fingerprinted
// (paid) request: forward the client's own UA, which carries its real name and
// version and so never goes stale. A generic SDK/HTTP-library UA is replaced
// with a neutral agent UA instead.
func resolveUserAgent(cfg Config, clientUA string) string {
	if ua := strings.TrimSpace(clientUA); ua != "" && !isGenericUserAgent(ua) {
		return ua
	}
	if v := strings.TrimSpace(cfg.UserAgent.FallbackValue); v != "" {
		return v
	}
	return DefaultFallbackUA
}

// isGenericUserAgent reports whether a User-Agent identifies an SDK / HTTP
// library (or is too short to be meaningful) rather than a real agent.
func isGenericUserAgent(ua string) bool {
	lower := strings.ToLower(strings.TrimSpace(ua))
	if len(lower) < 3 {
		return true
	}
	for _, p := range defaultGenericUAPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
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
