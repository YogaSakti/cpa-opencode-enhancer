package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// --- helpers -------------------------------------------------------------

func mustCall(t *testing.T, m *Manager, method string, payload any) Envelope {
	t.Helper()
	var raw []byte
	if payload != nil {
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
	}
	out, err := m.HandleCall(method, raw)
	if err != nil {
		t.Fatalf("HandleCall(%s): %v", method, err)
	}
	var env Envelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("decode envelope %s: %v (raw=%s)", method, err, out)
	}
	if !env.OK {
		msg := ""
		if env.Error != nil {
			msg = env.Error.Code + ": " + env.Error.Message
		}
		t.Fatalf("%s returned error envelope: %s", method, msg)
	}
	return env
}

func decodeResult[T any](t *testing.T, env Envelope) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatalf("decode result: %v (raw=%s)", err, env.Result)
	}
	return out
}

func newConfiguredManager(t *testing.T, yamlCfg string) *Manager {
	t.Helper()
	m := NewManager()
	cfg, err := LoadConfig([]byte(yamlCfg))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
	// Mirror production: register/reconfigure applies the logging switch.
	applyLoggingConfig(cfg)
	return m
}

// --- registration --------------------------------------------------------

func TestRegistrationAdvertisesCapabilities(t *testing.T) {
	m := NewManager()
	env := mustCall(t, m, MethodPluginRegister, LifecycleRequest{})
	reg := decodeResult[Registration](t, env)

	if reg.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", reg.SchemaVersion)
	}
	if !reg.Capabilities.RequestInterceptor {
		t.Fatalf("capabilities = %+v, want request_interceptor and scheduler", reg.Capabilities)
	}
	if reg.Metadata.GitHubRepository == "" {
		t.Fatal("GitHubRepository must be non-empty (host rejects empty)")
	}
	if reg.Metadata.Name != PluginID {
		t.Fatalf("name = %q, want %q", reg.Metadata.Name, PluginID)
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	m := NewManager()
	mustCall(t, m, MethodPluginShutdown, nil)
	mustCall(t, m, MethodPluginShutdown, nil)
}

func TestUnknownMethodReturnsErrorEnvelope(t *testing.T) {
	m := NewManager()
	out, err := m.HandleCall("nope.nope", nil)
	if err != nil {
		t.Fatalf("HandleCall: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.OK || env.Error == nil || env.Error.Code != "unknown_method" {
		t.Fatalf("env = %s, want unknown_method error", out)
	}
}

// --- session resolution --------------------------------------------------

func TestSessionFromCodexHeaderIsHashed(t *testing.T) {
	m := newConfiguredManager(t, "target:\n  base_url_markers: [opencode.ai]\n")
	req := RequestInterceptRequest{
		Model:        "kimi-k2.7-code",
		SourceFormat: "openai",
		Headers:      http.Header{"Session-Id": {"codex-conv-abc"}},
		Body:         []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		Metadata:     map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"},
	}
	env := mustCall(t, m, MethodRequestInterceptAfter, req)
	resp := decodeResult[RequestInterceptResponse](t, env)

	got := resp.Headers.Get("x-opencode-session")
	if got == "" {
		t.Fatal("x-opencode-session not injected")
	}
	if got == "codex-conv-abc" {
		t.Fatal("derived session must be hashed, got raw value")
	}
	if got != hashSession("codex-conv-abc") {
		t.Fatalf("session = %q, want sha256 of derived value", got)
	}
	if resp.Headers.Get(DefaultIdentityHeader) != "codex" {
		t.Fatalf("identity = %q, want codex", resp.Headers.Get(DefaultIdentityHeader))
	}
}

// TestSessionFallsBackToClientRequestID covers the last-resort source header:
// dsh's pi-ai openai-responses path stamps the conversation id there when the
// client's own affinity/session headers are withheld, so without it those
// requests carry no session at all.
// TestSessionSourcePrecedenceRankedLast pins the ordering: a client request id
// is a per-call value for most tools, so any recognized conversation header
// must outrank it.
func TestSessionSourcePrecedenceRankedLast(t *testing.T) {
	m := newConfiguredManager(t, "target:\n  base_url_markers: [opencode.ai]\n")
	req := RequestInterceptRequest{
		Model:        "deepseek-v4-flash",
		SourceFormat: "openai",
		Headers: http.Header{
			"X-Client-Request-Id":           {"per-call-noisy"},
			"X-DeepSeek-Harness-Session-Id": {"conversation-stable"},
		},
		Body:     []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"},
	}
	env := mustCall(t, m, MethodRequestInterceptAfter, req)
	resp := decodeResult[RequestInterceptResponse](t, env)

	got := resp.Headers.Get("x-opencode-session")
	if got != hashSession("conversation-stable") {
		t.Fatalf("session = %q, want the harness session header to win", got)
	}
}

// TestSessionAffinityOutranksGenericSessionID pins the ordering fix: the dsh
// pi-ai affinity header is conversation-scoped, the generic X-Session-Id may
// be stamped per call, so affinity must win when both are present.
func TestSessionAffinityOutranksGenericSessionID(t *testing.T) {
	m := newConfiguredManager(t, "target:\n  base_url_markers: [opencode.ai]\n")
	req := RequestInterceptRequest{
		Model:        "deepseek-v4-flash",
		SourceFormat: "openai",
		Headers: http.Header{
			"X-Session-Id":       {"generic-session"},
			"X-Session-Affinity": {"dsh-affinity"},
		},
		Body:     []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"},
	}
	env := mustCall(t, m, MethodRequestInterceptAfter, req)
	resp := decodeResult[RequestInterceptResponse](t, env)

	got := resp.Headers.Get("x-opencode-session")
	if got != hashSession("dsh-affinity") {
		t.Fatalf("session = %q, want the affinity header to win over generic X-Session-Id", got)
	}
}

// TestRequestIDFallbackDisabledByDefault pins that a request id is never
// injected as a session unless explicitly opted in: it is per-call, not a
// conversation id, so sending it upstream would destroy prompt-cache affinity.
// TestRequestIDFallbackWhenOptedIn verifies the escape hatch: enabling
// session.fallback_to_request_id injects the request id so a session-requiring
// upstream does not hard-fail when the body is unhashable.
func TestNativeSessionIsNeverOverriddenOrHashed(t *testing.T) {
	m := newConfiguredManager(t, "target:\n  base_url_markers: [opencode.ai]\n")
	req := RequestInterceptRequest{
		Model:    "glm-5.3",
		Headers:  http.Header{"X-Opencode-Session": {"native-session"}},
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"},
	}
	env := mustCall(t, m, MethodRequestInterceptAfter, req)
	resp := decodeResult[RequestInterceptResponse](t, env)

	if _, present := resp.Headers["X-Opencode-Session"]; present {
		t.Fatalf("native session must not be rewritten, headers = %#v", resp.Headers)
	}
}

func TestSessionFallsBackToBodyHash(t *testing.T) {
	m := newConfiguredManager(t, "")
	body := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello world"}]}`)
	req := RequestInterceptRequest{
		Model:        "glm-5.3",
		SourceFormat: "openai",
		Body:         body,
		Headers:      http.Header{},
		Metadata:     map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"},
	}
	env := mustCall(t, m, MethodRequestInterceptAfter, req)
	resp := decodeResult[RequestInterceptResponse](t, env)

	got := resp.Headers.Get("x-opencode-session")
	if got == "" {
		t.Fatal("body-hash session not injected")
	}
	if want := hashSession("hello world"); got != want {
		t.Fatalf("session = %q, want hash of first user content %q", got, want)
	}
}

func TestCanonicalSessionMetadataIsUsed(t *testing.T) {
	m := newConfiguredManager(t, "")
	req := RequestInterceptRequest{
		Model:   "deepseek-v4-flash",
		Body:    []byte(`{"messages":[{"role":"user","content":"x"}]}`),
		Headers: http.Header{},
		Metadata: map[string]any{
			"canonical_session_id": "claude:abc123",
			"selected_auth_id":     "openai-compatibility:opencode:1",
		},
	}
	env := mustCall(t, m, MethodRequestInterceptAfter, req)
	resp := decodeResult[RequestInterceptResponse](t, env)

	if got, want := resp.Headers.Get("x-opencode-session"), hashSession("claude:abc123"); got != want {
		t.Fatalf("session = %q, want %q", got, want)
	}
}

func TestFirstUserContentSkipsSystemPrompt(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"S"},{"role":"user","content":"U1"},{"role":"assistant","content":"A"},{"role":"user","content":"U2"}]}`)
	if got := firstUserContent("openai", body); got != "U1" {
		t.Fatalf("firstUserContent = %q, want %q", got, "U1")
	}
}

func TestFirstUserContentClaudeBlocks(t *testing.T) {
	body := []byte(`{"system":"sys","messages":[{"role":"user","content":[{"type":"text","text":"block text"}]}]}`)
	if got := firstUserContent("claude", body); got != "block text" {
		t.Fatalf("firstUserContent = %q, want %q", got, "block text")
	}
}

func TestFirstUserContentResponsesInput(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"resp text"}]}]}`)
	if got := firstUserContent("openai-response", body); got != "resp text" {
		t.Fatalf("firstUserContent = %q, want %q", got, "resp text")
	}
}

// --- user agent ----------------------------------------------------------

func TestUserAgentPassthroughForwardsClientUA(t *testing.T) {
	m := newConfiguredManager(t, "target:\n  base_url_markers: [opencode.ai]\n")
	req := RequestInterceptRequest{
		Model: "glm-5.3",
		Headers: http.Header{
			"X-Claude-Code-Session-Id": {"claude-conv-1"},
			"User-Agent":               {"claude-cli/2.3.1 (Mac OS X)"},
		},
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"},
	}
	env := mustCall(t, m, MethodRequestInterceptAfter, req)
	resp := decodeResult[RequestInterceptResponse](t, env)

	// Passthrough mode (default): forwards the client's own UA verbatim
	if got := resp.Headers.Get(DefaultOutboundUAHeader); got != "claude-cli/2.3.1 (Mac OS X)" {
		t.Fatalf("outbound UA = %q, want client UA passthrough", got)
	}
	if got := resp.Headers.Get(DefaultIdentityHeader); got != "claude-code" {
		t.Fatalf("identity = %q, want claude-code", got)
	}
}

func TestUserAgentPassthroughFallsBackForGenericUA(t *testing.T) {
	m := newConfiguredManager(t, "target:\n  base_url_markers: [opencode.ai]\n")
	req := RequestInterceptRequest{
		Model:    "glm-5.3",
		Headers:  http.Header{"User-Agent": {"Go-http-client/2.0"}},
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"},
	}
	env := mustCall(t, m, MethodRequestInterceptAfter, req)
	resp := decodeResult[RequestInterceptResponse](t, env)

	got := resp.Headers.Get(DefaultOutboundUAHeader)
	if got == "" {
		t.Fatal("UA must not be empty even with generic client UA")
	}
	if got == "Go-http-client/2.0" {
		t.Fatal("generic UA must not be forwarded")
	}
	// Should fall back to the neutral agent UA (no proxy marker)
	if got != DefaultFallbackUA {
		t.Fatalf("outbound UA = %q, want %q fallback", got, DefaultFallbackUA)
	}
	// Unidentified client: no X-Opencode-Client header (omit beats "cliproxy")
	if resp.Headers.Get(DefaultIdentityHeader) != "" {
		t.Fatalf("generic client must not get an identity header, got %q", resp.Headers.Get(DefaultIdentityHeader))
	}
}

func TestUserAgentFallbackValueOverride(t *testing.T) {
	m := newConfiguredManager(t, "user_agent:\n  fallback_value: my-agent/2.0\ntarget:\n  base_url_markers: [opencode.ai]\n")
	req := RequestInterceptRequest{
		Model:    "glm-5.3",
		Headers:  http.Header{"User-Agent": {"Go-http-client/2.0"}},
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"},
	}
	env := mustCall(t, m, MethodRequestInterceptAfter, req)
	resp := decodeResult[RequestInterceptResponse](t, env)

	if got := resp.Headers.Get(DefaultOutboundUAHeader); got != "my-agent/2.0" {
		t.Fatalf("outbound UA = %q, want configured fallback my-agent/2.0", got)
	}
}

func TestUserAgentNotRewrittenWhenDisabled(t *testing.T) {
	m := newConfiguredManager(t, "user_agent:\n  rewrite: false\ntarget:\n  base_url_markers: [opencode.ai]\n")
	req := RequestInterceptRequest{
		Model:    "glm-5.3",
		Headers:  http.Header{"Session-Id": {"s"}},
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"},
	}
	env := mustCall(t, m, MethodRequestInterceptAfter, req)
	resp := decodeResult[RequestInterceptResponse](t, env)

	if resp.Headers.Get(DefaultOutboundUAHeader) != "" {
		t.Fatalf("UA must not be set when rewrite disabled: %#v", resp.Headers)
	}
}

func TestDetectClientType(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
		want    string
	}{
		{"opencode native", http.Header{"X-Opencode-Session": {"s"}}, ClientOpenCode},
		{"claude", http.Header{"X-Claude-Code-Session-Id": {"s"}}, ClientClaude},
		{"codex", http.Header{"Session-Id": {"s"}}, ClientCodex},
		{"codex thread", http.Header{"Thread-Id": {"t"}}, ClientCodex},
		{"ua fallback", http.Header{"User-Agent": {"opencode/1.0"}}, ClientOpenCode},
		{"generic", http.Header{}, ClientGeneric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectClientType(tc.headers); got != tc.want {
				t.Fatalf("detectClientType = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- body cleanup --------------------------------------------------------

func TestIsZenFreeTierModelDynamicMarkers(t *testing.T) {
	cfg := FreeTierConfig{
		Markers:    []string{"-free", ":free"},
		FreeModels: []string{},
		PaidModels: []string{},
	}
	cases := map[string]bool{
		"muse-free":                       true,
		"opencode-go/muse-free":           true,
		"muse-spark-1.4-contributor-free": true,
		"muse-spark-1.3-contributor":      false,
		"glm-5.3":                         false,
		"deepseek-v4-flash-free":          true,
		"tencent/hy3:free":                true,
		"hy3:free":                        true,
		"":                                false,
	}
	for model, want := range cases {
		if got := isZenFreeTierModel(model, cfg); got != want {
			t.Fatalf("isZenFreeTierModel(%q) = %t, want %t", model, got, want)
		}
	}
}

func TestIsZenFreeTierModelPaidOverride(t *testing.T) {
	cfg := FreeTierConfig{
		Markers:    []string{"-free"},
		FreeModels: []string{},
		PaidModels: []string{"muse-spark-1.3-contributor-free"},
	}
	// Even though it ends in -free, the paid override wins
	if got := isZenFreeTierModel("muse-spark-1.3-contributor-free", cfg); got {
		t.Fatal("paid override must prevent stripping")
	}
}

func TestIsZenFreeTierModelExplicitFreeList(t *testing.T) {
	cfg := FreeTierConfig{
		Markers:    []string{},
		FreeModels: []string{"custom-free-model"},
		PaidModels: []string{},
	}
	if got := isZenFreeTierModel("custom-free-model", cfg); !got {
		t.Fatal("explicit zen_free_models entry must be treated as free")
	}
}

// --- targeting -----------------------------------------------------------

func TestNonTargetRequestIsNoOp(t *testing.T) {
	m := newConfiguredManager(t, "target:\n  base_url_markers: [opencode.ai]\n  auth_prefixes: [openai-compatibility:opencode:]\n")
	req := RequestInterceptRequest{
		Model:    "claude-sonnet-4",
		Headers:  http.Header{"Session-Id": {"s"}},
		Metadata: map[string]any{"selected_auth_id": "claude-api-key:***"},
	}
	env := mustCall(t, m, MethodRequestInterceptAfter, req)
	resp := decodeResult[RequestInterceptResponse](t, env)

	if len(resp.Headers) != 0 || len(resp.Body) != 0 {
		t.Fatalf("non-target request must be untouched, got %#v", resp)
	}
}

func TestTargetByModelGlob(t *testing.T) {
	m := newConfiguredManager(t, "target:\n  models: [\"muse-*\"]\n")
	req := RequestInterceptRequest{
		Model:    "muse-free",
		Headers:  http.Header{"Session-Id": {"s"}},
		Metadata: map[string]any{},
	}
	env := mustCall(t, m, MethodRequestInterceptAfter, req)
	resp := decodeResult[RequestInterceptResponse](t, env)

	if resp.Headers.Get("x-opencode-session") == "" {
		t.Fatal("model glob target did not trigger injection")
	}
}

// --- config --------------------------------------------------------------

func TestConfigValidationRejectsEmptyTargets(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Target = TargetConfig{}
	if err := validateConfig(cfg); err == nil {
		t.Fatal("empty target selectors must be rejected")
	}
}

func TestConfigValidationRejectsBadHeaderName(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Session.HeaderName = "bad\r\nname"
	if err := validateConfig(cfg); err == nil {
		t.Fatal("header name with CRLF must be rejected")
	}
}

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	cfg, err := LoadConfig([]byte("session:\n  header_name: X-Custom-Session\n  hash_derived: false\ntarget:\n  models: [\"a-*\"]\n"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Session.HeaderName != "X-Custom-Session" {
		t.Fatalf("header_name = %q", cfg.Session.HeaderName)
	}
	if BoolVal(cfg.Session.HashDerived, true) {
		t.Fatal("hash_derived override to false did not apply")
	}
	if cfg.UserAgent.OutboundHeader != DefaultOutboundUAHeader {
		t.Fatalf("outbound_header = %q, want default", cfg.UserAgent.OutboundHeader)
	}
}

func TestInterceptBeforeIsNoOp(t *testing.T) {
	m := newConfiguredManager(t, "")
	env := mustCall(t, m, MethodRequestInterceptBefore, RequestInterceptRequest{
		Model:   "glm-5.3",
		Headers: http.Header{"Session-Id": {"s"}},
	})
	resp := decodeResult[RequestInterceptResponse](t, env)
	if len(resp.Headers) != 0 {
		t.Fatalf("intercept_before must not return headers (host replaces, not merges): %#v", resp.Headers)
	}
}

func TestMalformedPayloadReturnsTransportError(t *testing.T) {
	m := NewManager()
	if _, err := m.HandleCall(MethodRequestInterceptAfter, []byte("{not json")); err == nil {
		t.Fatal("malformed payload should surface as a transport error")
	}
}

func TestSessionHeaderNameIsCustomizable(t *testing.T) {
	m := newConfiguredManager(t, "session:\n  header_name: X-OpenCode-Session\ntarget:\n  base_url_markers: [opencode.ai]\n")
	req := RequestInterceptRequest{
		Model:    "glm-5.3",
		Headers:  http.Header{"Session-Id": {"s1"}},
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"},
	}
	env := mustCall(t, m, MethodRequestInterceptAfter, req)
	resp := decodeResult[RequestInterceptResponse](t, env)

	if resp.Headers.Get("X-OpenCode-Session") == "" {
		t.Fatalf("custom session header not used: %#v", resp.Headers)
	}
}

// --- logging -------------------------------------------------------------

func TestLoggingOffByDefault(t *testing.T) {
	calls := 0
	SetHostCaller(func(string, []byte) ([]byte, error) { calls++; return nil, nil })
	defer SetHostCaller(nil)

	m := newConfiguredManager(t, "target:\n  base_url_markers: [opencode.ai]\n")
	req := RequestInterceptRequest{
		Model:    "glm-5.3",
		Headers:  http.Header{"Session-Id": {"s1"}},
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"},
	}
	mustCall(t, m, MethodRequestInterceptAfter, req)

	if calls != 0 {
		t.Fatalf("host.log must be OFF by default, got %d calls", calls)
	}
}

func TestLoggingEnabledWhenConfigured(t *testing.T) {
	var messages []string
	SetHostCaller(func(_ string, payload []byte) ([]byte, error) {
		var p struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(payload, &p)
		messages = append(messages, p.Message)
		return nil, nil
	})
	defer SetHostCaller(nil)

	m := newConfiguredManager(t, "logging:\n  enabled: true\ntarget:\n  base_url_markers: [opencode.ai]\n")
	req := RequestInterceptRequest{
		Model:    "glm-5.3",
		Headers:  http.Header{"Session-Id": {"s1"}},
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"},
	}
	mustCall(t, m, MethodRequestInterceptAfter, req)

	if len(messages) == 0 {
		t.Fatal("expected a shaped log line when logging.enabled: true")
	}
	if !strings.Contains(messages[0], "opencode-enhancer: shaped") {
		t.Fatalf("unexpected log message: %q", messages[0])
	}
}

// --- parseUserAgent / isGenericUserAgent ---------------------------------

func TestIsGenericUserAgent(t *testing.T) {
	cases := []struct {
		ua   string
		want bool
	}{
		{"Go-http-client/2.0", true},
		{"python-requests/2.31.0", true},
		{"curl/8.0.1", true},
		{"PostmanRuntime/7.36.0", true},
		{"codex-tui/0.153.3", false},
		{"claude-cli/2.2.0", false},
		{"opencode-client/1.0", false},
		{"", true},
		{"ab", true},
	}
	for _, tc := range cases {
		if got := isGenericUserAgent(tc.ua); got != tc.want {
			t.Fatalf("isGenericUserAgent(%q) = %t, want %t", tc.ua, got, tc.want)
		}
	}
}
