package plugin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// OpenCode Zen free-tier client fingerprint. The upstream gates the free tier
// on the *whole* set below; a request missing any one of them is rejected with
// HTTP 403 FreeTierError. Values match the official client (opencode 1.18.x).
const (
	// DefaultFingerprintUA must be >= 1.17; older versions are rejected.
	DefaultFingerprintUA      = "opencode/1.18.31"
	DefaultFingerprintClient  = "desktop"
	DefaultFingerprintProject = "global"
	// DefaultFingerprintAccept matches the official client, which always
	// streams. Paired with ForceStream.
	DefaultFingerprintAccept = "text/event-stream"

	HeaderOpenCodeProject = "X-Opencode-Project"
	HeaderOpenCodeRequest = "X-Opencode-Request"

	sessionIDPrefix = "ses_"
	requestIDPrefix = "msg_"
)

// base62Alphabet matches the official client's id alphabet.
const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// defaultFingerprintTools is the tool quartet the official client always
// declares. Zen uses its presence as part of the free-tier fingerprint.
var defaultFingerprintTools = []string{"bash", "glob", "grep", "read"}

// FingerprintConfig controls the OpenCode free-tier client fingerprint.
type FingerprintConfig struct {
	Enabled     *bool    `yaml:"enabled"`
	UserAgent   string   `yaml:"user_agent"`
	Client      string   `yaml:"client"`
	Project     string   `yaml:"project"`
	Accept      string   `yaml:"accept"`
	ForceStream *bool    `yaml:"force_stream"`
	InjectTools []string `yaml:"inject_tools"`
	// StripReasoning drops prior-turn reasoning items on the Responses path.
	// A pooled free-tier key cannot decrypt encrypted_content written by a
	// different upstream account, which returns HTTP 400.
	StripReasoning *bool `yaml:"strip_reasoning"`
	// WarnMissingGlue logs the credential headers: entries this plugin depends
	// on, once per credential. On by default: without them every request is a
	// silent 403.
	WarnMissingGlue *bool `yaml:"warn_missing_glue"`
	// FreeOnly restricts the fingerprint to free-tier models. Paid builds
	// authenticate normally and must not be reshaped.
	FreeOnly *bool `yaml:"free_only"`
}

// Fingerprint defaults.
const (
	DefaultFingerprintEnabled  = true
	DefaultFingerprintStream   = true
	DefaultFingerprintStripRsn = true
	DefaultFingerprintFreeOnly = true
	DefaultFingerprintWarnGlue = true
)

// defaultFingerprintConfig returns the default fingerprint configuration.
func defaultFingerprintConfig() FingerprintConfig {
	enabled := DefaultFingerprintEnabled
	stream := DefaultFingerprintStream
	stripReasoning := DefaultFingerprintStripRsn
	freeOnly := DefaultFingerprintFreeOnly
	warnGlue := DefaultFingerprintWarnGlue
	return FingerprintConfig{
		Enabled:         &enabled,
		UserAgent:       DefaultFingerprintUA,
		Client:          DefaultFingerprintClient,
		Project:         DefaultFingerprintProject,
		Accept:          DefaultFingerprintAccept,
		ForceStream:     &stream,
		InjectTools:     append([]string(nil), defaultFingerprintTools...),
		StripReasoning:  &stripReasoning,
		FreeOnly:        &freeOnly,
		WarnMissingGlue: &warnGlue,
	}
}

// fingerprintApplies reports whether the free-tier fingerprint should be
// applied to this request.
func fingerprintApplies(model, requestedModel string, cfg Config) bool {
	if !BoolVal(cfg.Fingerprint.Enabled, DefaultFingerprintEnabled) {
		return false
	}
	if !BoolVal(cfg.Fingerprint.FreeOnly, DefaultFingerprintFreeOnly) {
		return true
	}
	return isZenFreeTierModel(model, cfg.BodyClean) ||
		isZenFreeTierModel(requestedModel, cfg.BodyClean)
}

// fingerprintTools returns the configured tool quartet, or the default.
func fingerprintTools(cfg FingerprintConfig) []string {
	out := make([]string, 0, len(cfg.InjectTools))
	for _, name := range cfg.InjectTools {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return defaultFingerprintTools
	}
	return out
}

// fingerprintValue returns a trimmed config value or its default.
func fingerprintValue(v, fallback string) string {
	if t := strings.TrimSpace(v); t != "" {
		return t
	}
	return fallback
}

// outboundUAHeader returns the header the credential glue maps to User-Agent.
func outboundUAHeader(cfg Config) string {
	return fingerprintValue(cfg.UserAgent.OutboundHeader, DefaultOutboundUAHeader)
}

// boolLabel renders a bool for the log line.
func boolLabel(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// requiredGlueHeaders lists the credential `headers:` entries a fingerprinted
// request needs, in the "<declared name>: $<execution header>" form used in
// config.yaml.
//
// CLIProxyAPI never puts an execution header on the wire on its own: the
// executor iterates the credential's own `header:` attributes and uses the
// execution headers *only* to resolve a `$Name` reference
// (util.extractCustomHeaders). A header the credential does not declare is
// therefore dropped before the request leaves the proxy, however correctly
// this plugin set it.
func requiredGlueHeaders(cfg Config) []string {
	ua := outboundUAHeader(cfg)
	session := strings.TrimSpace(cfg.Session.HeaderName)
	if session == "" {
		session = DefaultSessionHeaderName
	}
	glue := []string{
		"User-Agent: \"$" + ua + "\"",
		http.CanonicalHeaderKey(session) + ": \"$" + http.CanonicalHeaderKey(session) + "\"",
		DefaultIdentityHeader + ": \"$" + DefaultIdentityHeader + "\"",
		HeaderOpenCodeProject + ": \"$" + HeaderOpenCodeProject + "\"",
		HeaderOpenCodeRequest + ": \"$" + HeaderOpenCodeRequest + "\"",
	}
	if strings.TrimSpace(cfg.Fingerprint.Accept) != "" {
		glue = append(glue, "Accept: \"$Accept\"")
	}
	return glue
}

// warnGlueRequirement emits one setup line per credential the first time it is
// fingerprinted. It bypasses logging.enabled on purpose: without the glue
// every request fails with 403 FreeTierError and nothing else in the logs
// points at the cause.
func (m *Manager) warnGlueRequirement(authID string, cfg Config) {
	if !BoolVal(cfg.Fingerprint.WarnMissingGlue, DefaultFingerprintWarnGlue) {
		return
	}
	key := strings.TrimSpace(authID)
	if key == "" {
		key = "(unknown auth)"
	}
	m.mu.Lock()
	if _, done := m.warnedAuths[key]; done {
		m.mu.Unlock()
		return
	}
	m.warnedAuths[key] = struct{}{}
	m.mu.Unlock()

	logToHostAlways("warn",
		"opencode-enhancer: free-tier fingerprint active for auth="+key+
			" — this credential's headers: map MUST declare all of: "+
			strings.Join(requiredGlueHeaders(cfg), ", ")+
			". Undeclared headers are dropped by the executor and upstream answers"+
			" 403 FreeTierError. Silence with fingerprint.warn_missing_glue: false.",
		nil)
}

// ── id shaping ──────────────────────────────────────────────────────────────

// openCodeSessionRE is the official client's session id shape. Zen rejects
// anything else on the free tier.
var openCodeSessionRE = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

// isOpenCodeSessionID reports whether a value already carries the official
// session id shape and can be forwarded verbatim.
func isOpenCodeSessionID(value string) bool {
	return openCodeSessionRE.MatchString(strings.TrimSpace(value))
}

// base62From maps raw bytes onto the official client's base62 alphabet.
func base62From(raw []byte) string {
	var sb strings.Builder
	sb.Grow(len(raw))
	for _, b := range raw {
		sb.WriteByte(base62Alphabet[int(b)%len(base62Alphabet)])
	}
	return sb.String()
}

// timeHexFrom renders the low 48 bits of value as 12 lowercase hex digits,
// matching the official client's id time component.
func timeHexFrom(value uint64) string {
	var sb strings.Builder
	sb.Grow(12)
	for i := 0; i < 6; i++ {
		sb.WriteString(fmt.Sprintf("%02x", byte(value>>(40-8*i))))
	}
	return sb.String()
}

// shapeSessionID turns any stable conversation identity into an id with the
// official `ses_<12 hex><14 base62>` shape. It is deterministic: the same
// conversation always yields the same upstream session, which preserves
// sticky routing and prompt-cache affinity.
//
// ponytail: the hex half is hash material, not the client's inverted
// timestamp. Zen validates the shape, not the ordering. If upstream ever
// starts decoding that timestamp, derive it from the conversation's
// first-seen time instead.
func shapeSessionID(identity string) string {
	digest := sha256.Sum256([]byte(sessionIDPrefix + identity))
	return sessionIDPrefix + hex.EncodeToString(digest[:6]) + base62From(digest[6:20])
}

// newRequestID returns a fresh per-request id with the official
// `msg_<12 hex><14 base62>` shape.
func newRequestID() string {
	current := uint64(time.Now().UnixMilli())*0x1000 + 1
	raw := make([]byte, 14)
	if _, err := rand.Read(raw); err != nil {
		// Randomness is a uniqueness aid here, not a security boundary:
		// fall back to the clock rather than failing the request.
		for i := range raw {
			raw[i] = byte(current >> (8 * (i % 8)))
		}
	}
	return requestIDPrefix + timeHexFrom(current) + base62From(raw)
}

// ── body shaping ────────────────────────────────────────────────────────────

// applyFingerprintBody rewrites a request body so it passes the Zen free-tier
// gates: streaming forced on, the official tool quartet declared, and (on the
// Responses path) no stored state or undecryptable prior reasoning.
// Returns the rewritten body and whether anything changed.
func applyFingerprintBody(body []byte, cfg FingerprintConfig) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return body, false
	}

	changed := false

	// Gate: stream:false is rejected with 403 even when every header is right.
	if BoolVal(cfg.ForceStream, DefaultFingerprintStream) {
		if v, ok := root["stream"].(bool); !ok || !v {
			root["stream"] = true
			changed = true
		}
	}

	tools := fingerprintTools(cfg)
	if _, isResponses := root["input"]; isResponses {
		// Responses path (muse-*): no server-side state on a pooled key.
		if v, ok := root["store"].(bool); !ok || v {
			root["store"] = false
			changed = true
		}
		if normalizeMaxOutputTokens(root) {
			changed = true
		}
		if BoolVal(cfg.StripReasoning, DefaultFingerprintStripRsn) {
			if stripReasoningItems(root) {
				changed = true
			}
		}
		if ensureTools(root, tools, responsesToolEntry) {
			changed = true
		}
	} else if ensureTools(root, tools, chatToolEntry) {
		changed = true
	}

	if !changed {
		return body, false
	}
	out, err := json.Marshal(root)
	if err != nil {
		return body, false
	}
	return out, true
}

// chatToolEntry builds a Chat Completions tool declaration.
func chatToolEntry(name string) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": "OpenCode built-in " + name + " tool",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

// responsesToolEntry builds a Responses API tool declaration (flat shape).
func responsesToolEntry(name string) map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        name,
		"description": "OpenCode built-in " + name + " tool",
		"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

// ensureTools appends any missing fingerprint tool to root["tools"], leaving
// the client's own tools untouched. Reports whether it added anything.
func ensureTools(root map[string]any, names []string, build func(string) map[string]any) bool {
	existing, _ := root["tools"].([]any)
	present := make(map[string]struct{}, len(existing))
	for _, tool := range existing {
		if name := toolName(tool); name != "" {
			present[name] = struct{}{}
		}
	}
	added := false
	for _, name := range names {
		if _, ok := present[name]; ok {
			continue
		}
		existing = append(existing, build(name))
		present[name] = struct{}{}
		added = true
	}
	if !added {
		return false
	}
	root["tools"] = existing
	return true
}

// toolName extracts a tool's name from either the chat or responses shape.
func toolName(tool any) string {
	m, ok := tool.(map[string]any)
	if !ok {
		return ""
	}
	if name, ok := m["name"].(string); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	fn, ok := m["function"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := fn["name"].(string)
	return strings.TrimSpace(name)
}

// normalizeMaxOutputTokens moves a chat-style token cap onto the Responses
// field name, which is the only one upstream accepts there.
func normalizeMaxOutputTokens(root map[string]any) bool {
	changed := false
	if _, ok := root["max_output_tokens"]; !ok {
		for _, key := range []string{"max_completion_tokens", "max_tokens"} {
			if v, ok := root[key].(float64); ok {
				root["max_output_tokens"] = v
				changed = true
				break
			}
		}
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		if _, ok := root[key]; ok {
			delete(root, key)
			changed = true
		}
	}
	return changed
}

// stripReasoningItems drops prior-turn reasoning entries and any encrypted
// reasoning payload from input[]. A pooled free-tier key cannot decrypt
// content written under a different upstream account (HTTP 400).
func stripReasoningItems(root map[string]any) bool {
	input, ok := root["input"].([]any)
	if !ok {
		return false
	}
	keep := make([]any, 0, len(input))
	changed := false
	for _, item := range input {
		m, ok := item.(map[string]any)
		if !ok {
			keep = append(keep, item)
			continue
		}
		if t, _ := m["type"].(string); strings.EqualFold(strings.TrimSpace(t), "reasoning") {
			changed = true
			continue
		}
		for _, key := range []string{"encrypted_content", "reasoning_encrypted_content"} {
			if _, ok := m[key]; ok {
				delete(m, key)
				changed = true
			}
		}
		keep = append(keep, item)
	}
	if !changed {
		return false
	}
	root["input"] = keep
	return true
}
