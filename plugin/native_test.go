package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func nativeCall(t *testing.T, m *Manager, method string, request any) Envelope {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := m.HandleCall(method, raw)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(response, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestNativeRegistrationAndAuthModels(t *testing.T) {
	m := NewManager()
	registration := nativeCall(t, m, "plugin.register", nil)
	var registered map[string]any
	if err := json.Unmarshal(registration.Result, &registered); err != nil {
		t.Fatal(err)
	}
	caps := registered["capabilities"].(map[string]any)
	for _, name := range []string{"auth_provider", "model_provider", "executor", "request_lifecycle_plugin"} {
		if caps[name] != true {
			t.Errorf("capability %s = %v", name, caps[name])
		}
	}
	if caps["model_router"] == true {
		t.Fatal("native models must use CPA's normal provider selection, including shared model IDs")
	}
	if caps["executor_model_scope"] != "oauth" {
		t.Errorf("scope = %v", caps["executor_model_scope"])
	}
	storage := []byte(`{"type":"opencode-enhancer","api_key":"test-secret","models":[{"name":"mimo-free","alias":"mimo"}]}`)
	parsed := nativeCall(t, m, "auth.parse", map[string]any{"Provider": PluginID, "Path": "/auth/zen.json", "FileName": "zen.json", "RawJSON": storage})
	if !parsed.OK {
		t.Fatalf("auth parse: %s", parsed.Error.Message)
	}
	var auth struct {
		Handled bool
		Auth    struct {
			Provider    string
			Attributes  map[string]string
			StorageJSON []byte
		}
	}
	if err := json.Unmarshal(parsed.Result, &auth); err != nil {
		t.Fatal(err)
	}
	if !auth.Handled || auth.Auth.Provider != PluginID || auth.Auth.Attributes["api_key"] != "test-secret" {
		t.Fatalf("unexpected parsed auth: handled=%t provider=%s", auth.Handled, auth.Auth.Provider)
	}
	for _, marker := range []string{"base_url", "compat_name", "provider_key"} {
		if auth.Auth.Attributes[marker] != "" {
			t.Fatalf("%s attribute makes CPA replace the plugin executor with OpenAI-compat transport", marker)
		}
	}
	models := nativeCall(t, m, "model.for_auth", map[string]any{"AuthProvider": PluginID, "StorageJSON": auth.Auth.StorageJSON})
	if !models.OK {
		t.Fatalf("models: %+v", models.Error)
	}
	var published struct {
		Provider string
		Models   []struct{ ID, Name string }
	}
	if err := json.Unmarshal(models.Result, &published); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, model := range published.Models {
		seen[model.ID] = model.Name
	}
	if seen["mimo"] != "mimo-free" || seen["mimo-free"] != "mimo-free" {
		t.Fatalf("published model mapping = %v", seen)
	}
}

func TestNativeAuthRejectsUntrustedEndpointAndPaidModels(t *testing.T) {
	m := NewManager()
	m.cfg.FreeTier.PaidModels = []string{"paid-model"}
	for _, extra := range []string{`,"base_url":"https://example.org/v1"`, `,"base_url":"http://opencode.ai/inference/openai/v1"`, `,"base_url":"https://opencode.ai:444/inference/openai/v1"`, `,"models":[{"name":"paid-model"}]`} {
		storage := []byte(`{"type":"opencode-enhancer","api_key":"secret"` + extra + `}`)
		envelope := nativeCall(t, m, "auth.parse", map[string]any{"Provider": PluginID, "RawJSON": storage})
		if envelope.OK {
			t.Errorf("accepted invalid auth %s", extra)
		}
		if envelope.Error != nil && strings.Contains(envelope.Error.Message, "secret") {
			t.Fatal("error leaked API key")
		}
	}
	unrelated := nativeCall(t, m, "auth.parse", map[string]any{"Provider": "other", "RawJSON": []byte(`{"type":"other"}`)})
	if !unrelated.OK || !strings.Contains(string(unrelated.Result), `"Handled":false`) {
		t.Fatalf("unrelated auth: %s", unrelated.Result)
	}
}

func TestNativeAuthRejectsResponsesOnlyMuse(t *testing.T) {
	storage := []byte(`{"type":"opencode-enhancer","api_key":"secret","models":[{"name":"muse-spark-contributor-free"}]}`)
	response := nativeCall(t, NewManager(), "auth.parse", map[string]any{"Provider": PluginID, "FileName": "zen.json", "RawJSON": storage})
	if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "Responses-only") {
		t.Fatalf("Muse native Chat restriction missing: %+v", response.Error)
	}
}

func TestNativeCountTokensIsExplicitlyUnsupported(t *testing.T) {
	response := nativeCall(t, NewManager(), "executor.count_tokens", map[string]any{})
	if response.OK || response.Error == nil {
		t.Fatal("token count must not fabricate a count")
	}
	raw, _ := json.Marshal(response.Error)
	if !strings.Contains(string(raw), `"http_status":501`) {
		t.Fatalf("unsupported count = %s", raw)
	}
	_ = http.StatusNotImplemented
}
