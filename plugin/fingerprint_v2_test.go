package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Removing fingerprint headers at either real request boundary must be caught,
// including a stale auth header that would otherwise survive native execution.
func TestV2NativeWireFingerprint(t *testing.T) {
	const session = "ses_123456789abcABCDEFGHIJKLMN"
	var traces []string
	m := nativeTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		assertV2Headers(t, r.Header, session)
		traces = append(traces, r.Header.Get("Traceparent"))
		if r.Header.Get("Authorization") != "Bearer upstream-key" || r.Header.Get("X-Custom") != "kept" {
			t.Error("native auth/custom header changed")
		}
		body, _ := io.ReadAll(r.Body)
		var root map[string]any
		if err := json.Unmarshal(body, &root); err != nil {
			t.Fatal(err)
		}
		if root["model"] != "mimo-free" || root["max_tokens"] != float64(37) || root["temperature"] != 0.2 || root["stream"] != true {
			t.Errorf("native model/limits/stream changed: %s", body)
		}
		options, _ := root["stream_options"].(map[string]any)
		if options["include_usage"] != true || options["client_option"] != "keep" {
			t.Errorf("native usage/options not preserved: %v", options)
		}
		messages, _ := root["messages"].([]any)
		if len(messages) != 4 || messages[0].(map[string]any)["role"] != "developer" {
			t.Errorf("environment inserted a new leading message: %v", messages)
		} else {
			text, _ := messages[0].(map[string]any)["content"].(string)
			cwd, _ := os.Getwd()
			if !strings.HasPrefix(text, "Keep developer instructions.\n\n") || !strings.Contains(text, "You are a coding agent.") || !strings.Contains(text, "Working directory: "+cwd) {
				t.Errorf("missing real working directory/coding marker: %q", text)
			}
			if messages[3].(map[string]any)["tool_call_id"] != "call-1" {
				t.Error("tool history changed")
			}
		}
		assertV2Tools(t, root["tools"].([]any), "chat")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n\n")
	})
	req := nativeTestRequest()
	req.Headers.Set("X-Opencode-Session-Id", session)
	req.StorageJSON = []byte(`{"type":"opencode-enhancer","api_key":"upstream-key","headers":{"X-Opencode-Request":"stale-msg","X-Custom":"kept"},"models":[{"name":"mimo-free","alias":"mimo"}]}`)
	req.Payload = []byte(`{"model":"mimo","max_tokens":37,"temperature":0.2,"stream":false,"stream_options":{"include_usage":false,"client_option":"keep"},"messages":[{"role":"developer","content":"Keep developer instructions."},{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call-1","content":"result"}],"tools":[{"type":"function","function":{"name":"read","description":"real client read","parameters":{"type":"object","properties":{"client_path":{"type":"string"}}}}}]}`)
	for i := 0; i < 2; i++ {
		response := nativeCall(t, m, "executor.execute", req)
		if !response.OK {
			t.Fatalf("execute: %+v", response.Error)
		}
	}
	if len(traces) != 2 || traces[0] == traces[1] {
		t.Error("per-request tracing identity reused")
	}
}

func TestV2LegacyFingerprintHeaders(t *testing.T) {
	m := NewManager()
	const session = "ses_123456789abcABCDEFGHIJKLMN"
	for _, primary := range []string{"", session} {
		req := RequestInterceptRequest{RequestID: "request-1", Model: "mimo-free", SourceFormat: "openai", Headers: http.Header{}, Body: []byte(`{"messages":[{"role":"user","content":"hi"}]}`), Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"}}
		req.Headers.Set("X-Opencode-Session-Id", "ses_111111111111abcdefghijklmn")
		if primary != "" {
			req.Headers.Set("X-Opencode-Session", primary)
		}
		payload, _ := json.Marshal(req)
		raw, err := m.HandleCall(MethodRequestInterceptAfter, payload)
		if err != nil {
			t.Fatal(err)
		}
		var env Envelope
		_ = json.Unmarshal(raw, &env)
		var resp RequestInterceptResponse
		_ = json.Unmarshal(env.Result, &resp)
		var serialized map[string]any
		_ = json.Unmarshal(env.Result, &serialized)
		clear, _ := serialized["ClearHeaders"].([]any)
		if len(clear) != 1 || clear[0] != "X-Opencode-Request" {
			t.Errorf("legacy result does not remove stale request header: %v", clear)
		}
		want := primary
		if want == "" {
			want = "ses_111111111111abcdefghijklmn"
		}
		assertV2Headers(t, resp.Headers, want)
	}
}

func TestV2PaidSessionAliasStillEmitsPrimary(t *testing.T) {
	const session = "ses_123456789abcABCDEFGHIJKLMN"
	req := RequestInterceptRequest{Model: "paid-model", Headers: http.Header{}, Body: []byte(`{}`), Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"}}
	req.Headers.Set("X-Opencode-Session-Id", session)
	payload, _ := json.Marshal(req)
	raw, err := NewManager().HandleCall(MethodRequestInterceptAfter, payload)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	_ = json.Unmarshal(raw, &envelope)
	var result map[string]any
	_ = json.Unmarshal(envelope.Result, &result)
	if _, exists := result["ClearHeaders"]; exists {
		t.Error("paid request received fingerprint header deletion")
	}
	var resp RequestInterceptResponse
	_ = json.Unmarshal(envelope.Result, &resp)
	if resp.Headers.Get("X-Opencode-Session") != session {
		t.Error("native session alias did not reach primary session header")
	}
}

func TestV2LegacyWithoutIdentityStillEmitsSessionFingerprint(t *testing.T) {
	req := RequestInterceptRequest{Model: "mimo-free", Headers: http.Header{}, Body: []byte(`{}`), Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"}}
	payload, _ := json.Marshal(req)
	raw, err := NewManager().HandleCall(MethodRequestInterceptAfter, payload)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	_ = json.Unmarshal(raw, &envelope)
	var resp RequestInterceptResponse
	_ = json.Unmarshal(envelope.Result, &resp)
	if !isOpenCodeSessionID(resp.Headers.Get("X-Opencode-Session")) {
		t.Error("missing last-resort session")
	}
	assertV2Headers(t, resp.Headers, resp.Headers.Get("X-Opencode-Session"))
}

func TestV2NativeAuthDropsDeprecatedRequestGlue(t *testing.T) {
	storage := []byte(`{"type":"opencode-enhancer","api_key":"secret","headers":{"x-opencode-request":"$X-Opencode-Request","X-Custom":"kept"}}`)
	response := nativeCall(t, NewManager(), "auth.parse", map[string]any{"Provider": PluginID, "FileName": "zen.json", "RawJSON": storage})
	if !response.OK {
		t.Fatalf("parse: %+v", response.Error)
	}
	var parsed struct {
		Auth struct{ Attributes map[string]string }
	}
	_ = json.Unmarshal(response.Result, &parsed)
	if parsed.Auth.Attributes["header:x-opencode-request"] != "" {
		t.Error("stale request glue became an auth header attribute")
	}
	if parsed.Auth.Attributes["header:X-Custom"] != "kept" {
		t.Error("custom header lost")
	}
}

func TestV2NativeProjectPrecedenceAndOverrides(t *testing.T) {
	const clientProject = "1234567890abcdef1234567890abcdef12345678"
	const authProject = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct{ name, configured, client, want string }{
		{"client", "", clientProject, clientProject},
		{"configured", "project-alpha", clientProject, "477cf088b95062eaa80dfb7ff45a6918c4b5754b"},
		{"auth fallback", "", "", authProject},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := nativeTestManager(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Opencode-Project") != tc.want {
					t.Errorf("project = %q, want %q", r.Header.Get("X-Opencode-Project"), tc.want)
				}
				if r.Header.Get("User-Agent") != "opencode/latest/custom/cli" || r.Header.Get("Accept") != "text/event-stream" {
					t.Error("explicit UA/Accept overrides ignored")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n\n")
			})
			m.cfg.Fingerprint.Project = tc.configured
			m.cfg.Fingerprint.UserAgent = "opencode/latest/custom/cli"
			m.cfg.Fingerprint.Accept = "text/event-stream"
			req := nativeTestRequest()
			req.Headers.Set("X-Opencode-Project", tc.client)
			req.StorageJSON = []byte(`{"type":"opencode-enhancer","api_key":"upstream-key","headers":{"X-Opencode-Project":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"models":[{"name":"mimo-free","alias":"mimo"}]}`)
			if response := nativeCall(t, m, "executor.execute", req); !response.OK {
				t.Fatalf("execute: %+v", response.Error)
			}
		})
	}
}

func TestV2LegacyProjectPrecedence(t *testing.T) {
	const project = "1234567890abcdef1234567890abcdef12345678"
	for _, tc := range []struct{ configured, want string }{{"", project}, {"project-alpha", "477cf088b95062eaa80dfb7ff45a6918c4b5754b"}} {
		m := NewManager()
		m.cfg.Fingerprint.Project = tc.configured
		req := RequestInterceptRequest{RequestID: "request-1", Model: "mimo-free", Headers: http.Header{}, Body: []byte(`{}`), Metadata: map[string]any{"selected_auth_id": "openai-compatibility:opencode:1"}}
		req.Headers.Set("X-Opencode-Project", project)
		payload, _ := json.Marshal(req)
		raw, err := m.HandleCall(MethodRequestInterceptAfter, payload)
		if err != nil {
			t.Fatal(err)
		}
		var envelope Envelope
		_ = json.Unmarshal(raw, &envelope)
		var resp RequestInterceptResponse
		_ = json.Unmarshal(envelope.Result, &resp)
		if resp.Headers.Get("X-Opencode-Project") != tc.want {
			t.Errorf("configured %q: project=%q, want %q", tc.configured, resp.Headers.Get("X-Opencode-Project"), tc.want)
		}
	}
}

func assertV2Headers(t *testing.T, headers http.Header, session string) {
	t.Helper()
	for name, want := range map[string]string{"User-Agent": "opencode/latest/2.2.5/cli", "Accept": "application/json, text/event-stream", "X-Opencode-Client": "cli", "X-Opencode-Session": session, "X-Opencode-Session-Id": session, "X-Session-Affinity": session, "X-Session-Id": session} {
		if got := headers.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(headers.Get("X-Opencode-Project")) {
		t.Errorf("invalid project hash: %q", headers.Get("X-Opencode-Project"))
	}
	if headers.Get("X-Opencode-Request") != "" {
		t.Error("deprecated request id leaked upstream")
	}
	trace := headers.Get("Traceparent")
	if !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`).MatchString(trace) {
		t.Errorf("invalid W3C trace: %q", trace)
	} else {
		b3 := headers.Get("B3")
		if !regexp.MustCompile(`^[0-9a-f]{32}-[0-9a-f]{16}-1-[0-9a-f]{16}$`).MatchString(b3) || !strings.HasPrefix(b3, strings.TrimPrefix(trace, "00-")[:49]+"-1-") {
			t.Errorf("b3 and traceparent do not identify the same trace/span: %q / %q", b3, trace)
		}
	}
}

func assertV2Tools(t *testing.T, tools []any, format string) {
	t.Helper()
	required := map[string][]string{"edit": {"path", "oldString", "newString"}, "glob": {"pattern"}, "grep": {"pattern"}, "read": {"path"}, "shell": {"command"}}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		if format == "chat" {
			tool = tool["function"].(map[string]any)
		}
		name, _ := tool["name"].(string)
		if name == "read" && tool["description"] == "real client read" {
			if tool["parameters"].(map[string]any)["properties"].(map[string]any)["client_path"] == nil {
				t.Error("real client tool schema replaced")
			}
			delete(required, name)
			continue
		}
		keys, known := required[name]
		if !known {
			t.Errorf("unexpected injected tool %q", name)
			continue
		}
		if tool["description"] != "Not available in this session. Do not call this tool." {
			t.Errorf("injected %s is advertised as executable", name)
		}
		params := tool["parameters"]
		if format == "claude" {
			params = tool["input_schema"]
		}
		schema := params.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		if name == "edit" {
			if replaceAll, _ := props["replaceAll"].(map[string]any); replaceAll["type"] != "boolean" {
				t.Error("edit cannot describe an all-occurrences replacement")
			}
		}
		for _, key := range keys {
			if props[key] == nil {
				t.Errorf("%s lacks realistic %s argument", name, key)
			}
		}
		if schema["additionalProperties"] != false || len(schema["required"].([]any)) != len(keys) {
			t.Errorf("%s has incomplete argument constraints: %v", name, schema)
		}
		delete(required, name)
	}
	if len(required) != 0 {
		t.Errorf("missing core tools: %v", required)
	}
}
