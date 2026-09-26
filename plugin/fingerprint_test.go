package plugin

import (
	"encoding/json"
	"net/http"
	"regexp"
	"testing"
)

func TestShapeSessionIDMatchesOfficialShape(t *testing.T) {
	for _, identity := range []string{"", "conv-1", "0123456789abcdef", "ses_not_valid"} {
		got := shapeSessionID(identity)
		if !isOpenCodeSessionID(got) {
			t.Fatalf("shapeSessionID(%q) = %q, does not match ses_ shape", identity, got)
		}
	}
	// Deterministic: same conversation must keep the same upstream session,
	// or sticky routing and prompt-cache affinity are lost.
	if shapeSessionID("conv-1") != shapeSessionID("conv-1") {
		t.Fatal("shapeSessionID is not deterministic")
	}
	if shapeSessionID("conv-1") == shapeSessionID("conv-2") {
		t.Fatal("distinct conversations collided")
	}
}

func TestNewRequestIDMatchesOfficialShape(t *testing.T) {
	re := regexp.MustCompile(`^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	first, second := newRequestID(), newRequestID()
	for _, id := range []string{first, second} {
		if !re.MatchString(id) {
			t.Fatalf("newRequestID() = %q, does not match %s", id, re)
		}
	}
	if first == second {
		t.Fatal("request ids must be unique per request")
	}
}

func TestIsOpenCodeSessionIDRejectsRawHash(t *testing.T) {
	// The pre-0.4.0 behaviour: a bare sha256 hex digest. Upstream rejects it.
	if isOpenCodeSessionID("895dd736aa1b4c3d895dd736aa1b4c3d895dd736aa1b4c3d895dd736aa1b4c3d") {
		t.Fatal("raw sha256 digest must not pass the ses_ shape check")
	}
}

func TestApplyFingerprintBodyChatPath(t *testing.T) {
	in := []byte(`{"model":"mimo-v2.5-free","stream":false,"tools":[{"type":"function","function":{"name":"bash"}}]}`)
	out, changed := applyFingerprintBody(in, defaultFingerprintConfig(), "")
	if !changed {
		t.Fatal("expected the chat body to be rewritten")
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("unmarshal rewritten body: %v", err)
	}
	if root["stream"] != true {
		t.Fatalf("stream = %v, want true (stream:false is a 403 gate)", root["stream"])
	}
	names := toolNames(t, root)
	for _, want := range defaultFingerprintTools {
		if _, ok := names[want]; !ok {
			t.Fatalf("tool %q missing from %v", want, names)
		}
	}
	if len(names) != len(defaultFingerprintTools) {
		t.Fatalf("client's own bash tool was duplicated: %v", names)
	}
	if _, ok := root["store"]; ok {
		t.Fatal("store must not be set on the chat path")
	}
}

func TestApplyFingerprintBodyResponsesPath(t *testing.T) {
	in := []byte(`{"model":"muse-spark-1.3-contributor-free","max_tokens":4096,"input":[` +
		`{"type":"reasoning","encrypted_content":"xx"},` +
		`{"type":"message","role":"user","encrypted_content":"yy","content":"hi"}]}`)
	out, changed := applyFingerprintBody(in, defaultFingerprintConfig(), "")
	if !changed {
		t.Fatal("expected the responses body to be rewritten")
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("unmarshal rewritten body: %v", err)
	}
	if root["store"] != false {
		t.Fatalf("store = %v, want false", root["store"])
	}
	if root["max_output_tokens"] != float64(4096) {
		t.Fatalf("max_output_tokens = %v, want 4096", root["max_output_tokens"])
	}
	if _, ok := root["max_tokens"]; ok {
		t.Fatal("max_tokens must be removed on the responses path")
	}
	input, _ := root["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input = %v, want the reasoning entry dropped", input)
	}
	item, _ := input[0].(map[string]any)
	if _, ok := item["encrypted_content"]; ok {
		t.Fatal("encrypted_content must be stripped: a pooled key cannot decrypt it")
	}
	for _, tool := range root["tools"].([]any) {
		m := tool.(map[string]any)
		if _, nested := m["function"]; nested {
			t.Fatal("responses tools must use the flat shape, not the chat shape")
		}
	}
}

func TestApplyFingerprintBodyIsIdempotent(t *testing.T) {
	cfg := defaultFingerprintConfig()
	first, changed := applyFingerprintBody([]byte(`{"model":"mimo-v2.5-free"}`), cfg, "")
	if !changed {
		t.Fatal("first pass should rewrite")
	}
	if _, changed := applyFingerprintBody(first, cfg, ""); changed {
		t.Fatal("second pass rewrote an already-shaped body")
	}
}

// codexLiteBody mirrors a Codex Desktop (Responses Lite) request: tool
// declarations arrive as an input[0] additional_tools item.
const codexLiteBody = `{"model":"muse-spark-1.3-contributor-free","stream":true,"store":false,` +
	`"tools":[{"type":"function","name":"shell","description":"top-level"}],"input":[` +
	`{"type":"additional_tools","role":"developer","tools":[` +
	`{"type":"custom","name":"exec"},` +
	`{"type":"namespace","name":"mcp__exa","tools":[{"type":"function","name":"search"}]},` +
	`{"type":"function","name":"shell","description":"nested"},` +
	`{"type":"function","name":"view_image","parameters":{"type":"object"}},` +
	`"not-an-object"]},` +
	`{"type":"message","role":"user","content":"hi"},` +
	`{"type":"function_call_output","call_id":"c1","output":"ok"}]}`

func TestApplyFingerprintBodyStripsAdditionalToolsOnResponsesWire(t *testing.T) {
	cfg := defaultFingerprintConfig()
	out, changed := applyFingerprintBody([]byte(codexLiteBody), cfg, "codex")
	if !changed {
		t.Fatal("expected additional_tools to be stripped for a codex upstream")
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("unmarshal rewritten body: %v", err)
	}
	input, _ := root["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input = %v, want only the message and function_call_output", input)
	}
	for i, want := range []string{"message", "function_call_output"} {
		if got := input[i].(map[string]any)["type"]; got != want {
			t.Fatalf("input[%d].type = %v, want %q (order must be preserved)", i, got, want)
		}
	}

	names := toolNames(t, root)
	for _, want := range append([]string{"shell", "view_image"}, defaultFingerprintTools...) {
		if _, ok := names[want]; !ok {
			t.Fatalf("tool %q missing from %v", want, names)
		}
	}
	for _, gone := range []string{"exec", "mcp__exa", "search"} {
		if _, ok := names[gone]; ok {
			t.Fatalf("tool %q has no verified top-level shape and must not be promoted", gone)
		}
	}
	for _, tool := range root["tools"].([]any) {
		m := tool.(map[string]any)
		if m["name"] == "shell" && m["description"] != "top-level" {
			t.Fatalf("shell = %v, want the top-level declaration to win", m)
		}
	}
	if len(root["tools"].([]any)) != len(names) {
		t.Fatalf("tools contain duplicate names: %v", root["tools"])
	}

	if _, changed := applyFingerprintBody(out, cfg, "codex"); changed {
		t.Fatal("second pass rewrote an already-stripped body")
	}
}

func TestApplyFingerprintBodyKeepsAdditionalToolsForTranslatedTargets(t *testing.T) {
	disabled, err := LoadConfig([]byte("fingerprint:\n  strip_additional_tools: false\n"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cases := []struct {
		name     string
		cfg      FingerprintConfig
		toFormat string
	}{
		// The host's translators convert additional_tools into native tools.
		{"chat target", defaultFingerprintConfig(), "openai"},
		{"claude target", defaultFingerprintConfig(), "claude"},
		{"unknown target", defaultFingerprintConfig(), ""},
		{"disabled", disabled.Fingerprint, "codex"},
	}
	for _, tc := range cases {
		out, _ := applyFingerprintBody([]byte(codexLiteBody), tc.cfg, tc.toFormat)
		var root map[string]any
		if err := json.Unmarshal(out, &root); err != nil {
			t.Fatalf("%s: unmarshal body: %v", tc.name, err)
		}
		input, _ := root["input"].([]any)
		if len(input) != 3 || input[0].(map[string]any)["type"] != "additional_tools" {
			t.Fatalf("%s: input = %v, want additional_tools kept", tc.name, input)
		}
	}
}

// Muse runs on a codex-api-key credential, whose auth id names no provider,
// so the operator targets it by model glob.
func TestInterceptAfterStripsAdditionalToolsForMuseFree(t *testing.T) {
	m := newConfiguredManager(t, "target:\n  models: [\"muse-*\"]\n")
	req := RequestInterceptRequest{
		RequestID:      "req-muse-lite",
		SourceFormat:   "openai-response",
		ToFormat:       "codex",
		Model:          "muse-spark-1.3-contributor-free",
		RequestedModel: "muse-free(high)",
		Headers:        http.Header{"Session-Id": []string{"conv-abc"}},
		Body:           []byte(codexLiteBody),
		Metadata:       map[string]any{"selected_auth_id": "codex:apikey:5faa5798d3a4"},
	}
	payload, _ := json.Marshal(req)
	raw, err := m.HandleCall(MethodRequestInterceptAfter, payload)
	if err != nil {
		t.Fatalf("HandleCall: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	var resp RequestInterceptResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal shaped body: %v", err)
	}
	input, _ := body["input"].([]any)
	if len(input) == 0 || input[0].(map[string]any)["type"] != "message" {
		t.Fatalf("input = %v, want additional_tools gone from input[0]", input)
	}
}

func TestFingerprintAppliesOnlyToFreeModels(t *testing.T) {
	cfg := DefaultConfig()
	if !fingerprintApplies(RequestInterceptRequest{Model: "mimo-v2.5-free"}, cfg) {
		t.Fatal("free model must get the fingerprint")
	}
	if fingerprintApplies(RequestInterceptRequest{
		Model:    "muse-spark-1.3-contributor",
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode zen:1"},
	}, cfg) {
		t.Fatal("paid build must not be reshaped")
	}
	disabled := false
	cfg.Fingerprint.Enabled = &disabled
	if fingerprintApplies(RequestInterceptRequest{Model: "mimo-v2.5-free"}, cfg) {
		t.Fatal("fingerprint.enabled=false must disable the path")
	}
}

func TestFingerprintAppliesToMuseContributorOnOpenCodeGo(t *testing.T) {
	cfg := DefaultConfig()
	req := RequestInterceptRequest{
		Model:    "muse-spark-1.3-contributor",
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode go:1"},
	}
	if !fingerprintApplies(req, cfg) {
		t.Fatal("OpenCode Go Muse Contributor must get the free-tier fingerprint automatically")
	}

	cfg.FreeTier.PaidModels = []string{"muse-spark-1.3-contributor"}
	if fingerprintApplies(req, cfg) {
		t.Fatal("paid_models override must disable the automatic Go Muse fingerprint")
	}
}

// TestInterceptAfterFreeTierFingerprint is the end-to-end check: a free-tier
// request must leave with every header the Zen gate gets checked on.
func TestInterceptAfterFreeTierFingerprint(t *testing.T) {
	m := NewManager()
	req := RequestInterceptRequest{
		RequestID: "req-1",
		Model:     "mimo-v2.5-free",
		Headers: http.Header{
			"User-Agent": []string{"codex-tui/0.153.3"},
			"Session-Id": []string{"conv-abc"},
		},
		Body:     []byte(`{"model":"mimo-v2.5-free","stream":false}`),
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode go:1"},
	}
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	raw, err := m.HandleCall(MethodRequestInterceptAfter, payload)
	if err != nil {
		t.Fatalf("HandleCall: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("envelope not ok: %+v", env.Error)
	}
	var resp RequestInterceptResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got := resp.Headers.Get("User-Agent"); got != DefaultFingerprintUA {
		t.Fatalf("User-Agent = %q, want %q (client UA fails the gate)", got, DefaultFingerprintUA)
	}
	if got := resp.Headers.Get("X-Opencode-Client"); got != DefaultFingerprintClient {
		t.Fatalf("X-Opencode-Client = %q, want %q", got, DefaultFingerprintClient)
	}
	if got := resp.Headers.Get("X-Opencode-Project"); got != DefaultFingerprintProject {
		t.Fatalf("X-Opencode-Project = %q, want %q", got, DefaultFingerprintProject)
	}
	if got := resp.Headers.Get("X-Opencode-Request"); got == "" {
		t.Fatal("X-Opencode-Request is missing")
	}
	if got := resp.Headers.Get("x-opencode-session"); !isOpenCodeSessionID(got) {
		t.Fatalf("x-opencode-session = %q, want the ses_ shape", got)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal shaped body: %v", err)
	}
	if body["stream"] != true {
		t.Fatalf("stream = %v, want true", body["stream"])
	}
	if len(body["tools"].([]any)) != len(defaultFingerprintTools) {
		t.Fatalf("tools = %v, want the quartet", body["tools"])
	}
}

// TestInterceptAfterPaidMuseModelUntouched guards the paid Zen path: no
// fingerprint, no forced streaming, no injected tools.
func TestInterceptAfterPaidMuseModelUntouched(t *testing.T) {
	m := NewManager()
	req := RequestInterceptRequest{
		RequestID: "req-2",
		Model:     "muse-spark-1.3-contributor",
		Headers: http.Header{
			"User-Agent": []string{"codex-tui/0.153.3"},
			"Session-Id": []string{"conv-abc"},
		},
		Body:     []byte(`{"model":"muse-spark-1.3-contributor","stream":false}`),
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode zen:1"},
	}
	payload, _ := json.Marshal(req)
	raw, err := m.HandleCall(MethodRequestInterceptAfter, payload)
	if err != nil {
		t.Fatalf("HandleCall: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	var resp RequestInterceptResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(resp.Body) != 0 {
		t.Fatalf("paid request body was rewritten: %s", resp.Body)
	}
	if got := resp.Headers.Get("X-Opencode-Project"); got != "" {
		t.Fatalf("paid request got fingerprint header X-Opencode-Project=%q", got)
	}
	if got := resp.Headers.Get("X-Opencode-User-Agent"); got != "codex-tui/0.153.3" {
		t.Fatalf("paid request UA = %q, want the client's own UA passed through", got)
	}
}

func TestInterceptAfterGoMuseContributorFingerprint(t *testing.T) {
	m := NewManager()
	req := RequestInterceptRequest{
		RequestID: "req-go-muse",
		Model:     "muse-spark-1.3-contributor",
		Headers: http.Header{
			"User-Agent": []string{"codex-tui/0.153.3"},
			"Session-Id": []string{"conv-abc"},
		},
		Body:     []byte(`{"model":"muse-spark-1.3-contributor","stream":false,"input":"hi"}`),
		Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode go:1"},
	}
	payload, _ := json.Marshal(req)
	raw, err := m.HandleCall(MethodRequestInterceptAfter, payload)
	if err != nil {
		t.Fatalf("HandleCall: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	var resp RequestInterceptResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got := resp.Headers.Get("User-Agent"); got != DefaultFingerprintUA {
		t.Fatalf("User-Agent = %q, want %q", got, DefaultFingerprintUA)
	}
	if got := resp.Headers.Get("x-opencode-session"); !isOpenCodeSessionID(got) {
		t.Fatalf("x-opencode-session = %q, want the ses_ shape", got)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal shaped body: %v", err)
	}
	if body["stream"] != true {
		t.Fatalf("stream = %v, want true", body["stream"])
	}
	if len(body["tools"].([]any)) != len(defaultFingerprintTools) {
		t.Fatalf("tools = %v, want the quartet", body["tools"])
	}
}

func toolNames(t *testing.T, root map[string]any) map[string]struct{} {
	t.Helper()
	tools, ok := root["tools"].([]any)
	if !ok {
		t.Fatalf("tools missing or not an array: %v", root["tools"])
	}
	names := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if name := toolName(tool); name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}
