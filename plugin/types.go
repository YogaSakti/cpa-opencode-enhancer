package plugin

import (
	"encoding/json"
	"net/http"
)

// Envelope is the RPC response wrapper used by the CLIProxyAPI plugin ABI.
type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *EnvelopeError  `json:"error,omitempty"`
}

// EnvelopeError carries an error code and message.
type EnvelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RequestInterceptRequest mirrors the fields of pluginapi.RequestInterceptRequest
// that this plugin reads. The host marshals pluginapi structs with Go field
// names (no json tags), so the JSON keys below match those exactly.
type RequestInterceptRequest struct {
	RequestID      string         `json:"RequestID"`
	SourceFormat   string         `json:"SourceFormat"`
	ToFormat       string         `json:"ToFormat"` // upstream protocol, e.g. "codex"
	Model          string         `json:"Model"`
	RequestedModel string         `json:"RequestedModel"`
	Headers        http.Header    `json:"Headers"`
	Body           []byte         `json:"Body"`
	Metadata       map[string]any `json:"Metadata"`
}

// RequestInterceptResponse mirrors pluginapi.RequestInterceptResponse.
type RequestInterceptResponse struct {
	Headers http.Header `json:"Headers,omitempty"`
	Body    []byte      `json:"Body,omitempty"`
}

// Metadata mirrors pluginapi.Metadata.
type Metadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	ConfigFields     []ConfigField `json:"ConfigFields"`
}

// ConfigField mirrors pluginapi.ConfigField.
type ConfigField struct {
	Name        string `json:"Name"`
	Type        string `json:"Type"`
	Description string `json:"Description"`
}

// Capabilities declares the plugin's supported integration points.
type Capabilities struct {
	RequestInterceptor bool `json:"request_interceptor"`
}

// Registration is the plugin.register / plugin.reconfigure result.
type Registration struct {
	SchemaVersion uint32       `json:"schema_version"`
	Metadata      Metadata     `json:"metadata"`
	Capabilities  Capabilities `json:"capabilities"`
}

// LifecycleRequest mirrors the host's lifecycle RPC request.
type LifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

// OKEnvelope wraps a result into a success envelope.
func OKEnvelope(result any) []byte {
	raw, err := json.Marshal(result)
	if err != nil {
		return ErrEnvelope("plugin_error", "encode result failed")
	}
	out, err := json.Marshal(Envelope{OK: true, Result: raw})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"encode envelope failed"}}`)
	}
	return out
}

// ErrEnvelope builds an error envelope.
func ErrEnvelope(code, message string) []byte {
	out, err := json.Marshal(Envelope{OK: false, Error: &EnvelopeError{Code: code, Message: message}})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"encode error envelope failed"}}`)
	}
	return out
}

// emptyInterceptResponse is the no-op response.
func emptyInterceptResponse() []byte {
	return OKEnvelope(RequestInterceptResponse{})
}
