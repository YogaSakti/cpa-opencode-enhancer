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
	out, changed := applyFingerprintBody(in, defaultFingerprintConfig())
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
	out, changed := applyFingerprintBody(in, defaultFingerprintConfig())
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
	first, changed := applyFingerprintBody([]byte(`{"model":"mimo-v2.5-free"}`), cfg)
	if !changed {
		t.Fatal("first pass should rewrite")
	}
	if _, changed := applyFingerprintBody(first, cfg); changed {
		t.Fatal("second pass rewrote an already-shaped body")
	}
}

func TestFingerprintAppliesOnlyToFreeModels(t *testing.T) {
	cfg := DefaultConfig()
	if !fingerprintApplies("mimo-v2.5-free", "", cfg) {
		t.Fatal("free model must get the fingerprint")
	}
	if fingerprintApplies("muse-spark-1.3-contributor", "", cfg) {
		t.Fatal("paid build must not be reshaped")
	}
	disabled := false
	cfg.Fingerprint.Enabled = &disabled
	if fingerprintApplies("mimo-v2.5-free", "", cfg) {
		t.Fatal("fingerprint.enabled=false must disable the path")
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
		Metadata: map[string]any{"base_url": "https://opencode.ai/zen/go/v1"},
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

// TestInterceptAfterPaidModelUntouched guards the paid path: no fingerprint,
// no forced streaming, no injected tools.
func TestInterceptAfterPaidModelUntouched(t *testing.T) {
	m := NewManager()
	req := RequestInterceptRequest{
		RequestID: "req-2",
		Model:     "muse-spark-1.3-contributor",
		Headers: http.Header{
			"User-Agent": []string{"codex-tui/0.153.3"},
			"Session-Id": []string{"conv-abc"},
		},
		Body:     []byte(`{"model":"muse-spark-1.3-contributor","stream":false}`),
		Metadata: map[string]any{"base_url": "https://opencode.ai/zen/v1"},
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
