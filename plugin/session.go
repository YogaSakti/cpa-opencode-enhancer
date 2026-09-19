package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// maxSessionLen bounds accepted session values (defensive: header injection).
const maxSessionLen = 512

// sessionResult reports where a session value came from.
type sessionResult struct {
	Value  string
	Source string // "native", "metadata", "header", "body", "request"
	Stable bool   // false for request-scoped fallbacks
}

// resolveSessionID resolves the OpenCode session identity for one request.
//
// Precedence (first non-empty wins):
//  1. native x-opencode-session header — authoritative, never rewritten
//  2. CLAUDE/Codex/… session headers, in configured order
//  3. canonical_session_id metadata (host-computed session identity)
//  4. hash of the first user turn body content
//
// Derived values are hashed when session.hash_derived is enabled; native
// values pass through untouched.
func resolveSessionID(req RequestInterceptRequest, cfg Config, headerName string) (sessionResult, bool) {
	// 1. Native header: the real OpenCode client is authoritative. Values that
	// cannot be forwarded (CRLF, oversized) fall through to the derived
	// sources rather than being sent as a malformed upstream header.
	if v, ok := headerValue(req.Headers, headerName); ok {
		if cleaned := cleanSession(v); cleaned != "" {
			return sessionResult{Value: cleaned, Source: "native", Stable: true}, true
		}
	}

	// 2. Configured source headers.
	for _, src := range cfg.Session.SourceHeaders {
		if strings.EqualFold(src, headerName) {
			continue
		}
		if v, ok := headerValue(req.Headers, src); ok {
			if cleaned := cleanSession(v); cleaned != "" {
				return sessionResult{Value: cleaned, Source: "header", Stable: true}, true
			}
		}
	}

	// 3. Host-computed canonical session identity.
	if v := metadataString(req.Metadata, "canonical_session_id"); v != "" {
		if cleaned := cleanSession(v); cleaned != "" {
			return sessionResult{Value: cleaned, Source: "metadata", Stable: true}, true
		}
	}

	// 4. Body-derived identity (hash of the first user turn).
	if BoolVal(cfg.Session.FallbackBodyHash, DefaultFallbackBodyHash) {
		if content := firstUserContent(req.SourceFormat, req.Body); content != "" {
			digest := sha256.Sum256([]byte(content))
			return sessionResult{Value: hex.EncodeToString(digest[:]), Source: "body", Stable: true}, true
		}
	}

	return sessionResult{}, false
}

// sessionHeaderValue renders the value to send upstream. Raw client-provided
// identities (headers, canonical metadata) are hashed when
// session.hash_derived is enabled so the client's own session id never leaves
// the proxy. Values that are already opaque — the native OpenCode header, a
// content digest, or a per-call request id — pass through untouched.
func sessionHeaderValue(res sessionResult, cfg Config) string {
	switch res.Source {
	case "native", "body", "request":
		return res.Value
	}
	if BoolVal(cfg.Session.HashDerived, DefaultHashDerived) {
		return hashSession(res.Value)
	}
	return res.Value
}

// hashSession returns a stable, privacy-preserving session identity.
func hashSession(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// cleanSession trims and rejects values that are unsafe as header values.
func cleanSession(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxSessionLen {
		return ""
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

// firstUserContent extracts the text of the leading user turn(s) so a stable
// hash can be derived when no client session header exists. System/developer
// messages are skipped; scanning stops at the first non-user message after
// the user run began.
func firstUserContent(sourceFormat string, body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return ""
	}
	var sb strings.Builder
	switch strings.ToLower(strings.TrimSpace(sourceFormat)) {
	case "openai":
		collectMessageTurns(root, &sb, false)
	case "claude":
		collectMessageTurns(root, &sb, true)
	case "openai-response":
		collectResponsesTurns(root, &sb)
	default:
		// Unknown client protocol: try every shape, first hit wins.
		if collectMessageTurns(root, &sb, false) {
			return sb.String()
		}
		sb.Reset()
		if collectMessageTurns(root, &sb, true) {
			return sb.String()
		}
		sb.Reset()
		if collectResponsesTurns(root, &sb) {
			return sb.String()
		}
	}
	if sb.Len() == 0 {
		return ""
	}
	return sb.String()
}

// collectMessageTurns walks a messages[] array and writes the leading user
// turn(s) into sb. lenient controls what happens to a non-user message found
// before the user run starts: lenient (Claude — system lives at the top level)
// keeps scanning, strict (OpenAI — a leading assistant/tool message means
// there is no user run) breaks out. System/developer preambles are always
// skipped so the same conversation hashes identically across turns.
func collectMessageTurns(root map[string]any, sb *strings.Builder, lenient bool) bool {
	messages, ok := root["messages"].([]any)
	if !ok {
		return false
	}
	started := false
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if !started {
			switch {
			case role == "user":
				started = true
			case lenient || role == "system" || role == "developer":
				continue
			default:
				return false
			}
		} else if role != "user" {
			break
		}
		appendContentParts(msg["content"], sb)
	}
	return started
}

func collectResponsesTurns(root map[string]any, sb *strings.Builder) bool {
	input := root["input"]
	started := false
	switch typed := input.(type) {
	case string:
		sb.WriteString(typed)
		return typed != ""
	case []any:
		for _, raw := range typed {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			role, _ := item["role"].(string)
			itemType, _ := item["type"].(string)
			isUser := role == "user" && (itemType == "message" || itemType == "")
			if !started {
				if !isUser {
					continue
				}
				started = true
			} else if !isUser {
				break
			}
			appendContentParts(item["content"], sb)
		}
	}
	return started
}

// appendContentParts writes string or structured content blocks into sb.
func appendContentParts(content any, sb *strings.Builder) {
	switch typed := content.(type) {
	case string:
		sb.WriteString(typed)
	case []any:
		for _, raw := range typed {
			part, ok := raw.(map[string]any)
			if !ok {
				if s, ok := raw.(string); ok {
					sb.WriteString(s)
				}
				continue
			}
			if text, ok := part["text"].(string); ok {
				sb.WriteString(text)
				continue
			}
			// Image/document blocks: use the URL or source data so two
			// conversations that differ only by attachment stay distinct.
			if url, ok := part["image_url"].(string); ok {
				sb.WriteString(url)
				continue
			}
			if src, ok := part["source"].(map[string]any); ok {
				if data, ok := src["data"].(string); ok {
					sb.WriteString(data)
				} else if url, ok := src["url"].(string); ok {
					sb.WriteString(url)
				}
			}
		}
	}
}
