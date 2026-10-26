package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

const nativeBaseURL = "https://opencode.ai/inference/openai/v1"
const nativeExecutionIDHeader = "X-CPA-OpenCode-Execution-ID"
const nativeSourceFormatHeader = "X-CPA-OpenCode-Source-Format"

type nativeModel struct {
	Name  string `json:"name"`
	Alias string `json:"alias,omitempty"`
}
type nativeAuth struct {
	Type     string            `json:"type"`
	APIKey   string            `json:"api_key"`
	BaseURL  string            `json:"base_url"`
	ProxyURL string            `json:"proxy_url"`
	Prefix   string            `json:"prefix"`
	Label    string            `json:"label"`
	Disabled bool              `json:"disabled"`
	Headers  map[string]string `json:"headers"`
	Models   []nativeModel     `json:"models"`
}
type nativeAuthData struct {
	Provider, ID, FileName, Label, Prefix, ProxyURL string
	Disabled                                        bool
	StorageJSON                                     []byte
	Metadata                                        map[string]any
	Attributes                                      map[string]string
}
type nativeExecutorRequest struct {
	AuthID, AuthProvider, Model, Format, SourceFormat string
	Stream                                            bool
	Headers                                           http.Header
	Payload, OriginalRequest, StorageJSON             []byte
	Metadata, AuthMetadata                            map[string]any
	AuthAttributes                                    map[string]string
	StreamID                                          string `json:"stream_id"`
	HostCallbackID                                    string `json:"host_callback_id"`
}

type nativeExecution struct{ cancel context.CancelFunc }

func nativeError(code, message string, status int) []byte {
	raw, _ := json.Marshal(Envelope{Error: &EnvelopeError{Code: code, Message: message, HTTPStatus: status}})
	return raw
}

func validateNativeURL(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return nativeBaseURL, nil
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "opencode.ai") || (u.Port() != "" && u.Port() != "443") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "/inference/openai/v1" && u.Path != "/inference/v1") {
		return "", errors.New("native OpenCode base_url must be the official HTTPS inference endpoint")
	}
	return u.String(), nil
}

func (m *Manager) decodeNativeAuth(raw []byte) (nativeAuth, error) {
	var auth nativeAuth
	if err := json.Unmarshal(raw, &auth); err != nil {
		return auth, errors.New("invalid OpenCode auth JSON")
	}
	if auth.Type != PluginID {
		return auth, errors.New("unexpected OpenCode auth type")
	}
	auth.APIKey = strings.TrimSpace(auth.APIKey)
	if auth.APIKey == "" {
		return auth, errors.New("OpenCode api_key is required")
	}
	base, err := validateNativeURL(auth.BaseURL)
	if err != nil {
		return auth, err
	}
	auth.BaseURL = base
	auth.ProxyURL = normalizeNativeProxy(auth.ProxyURL)
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	seen := map[string]string{}
	for i := range auth.Models {
		model := &auth.Models[i]
		model.Name = strings.TrimSpace(model.Name)
		model.Alias = strings.TrimSpace(model.Alias)
		if strings.HasPrefix(lastSegment(model.Name), "muse-") {
			return auth, errors.New("Responses-only Muse models are unsupported by the native Chat executor")
		}
		if model.Name == "" || strings.ContainsAny(model.Name+model.Alias, "\r\n") || isExplicitPaidModel(lastSegment(model.Name), cfg.FreeTier) {
			return auth, fmt.Errorf("invalid or explicitly paid OpenCode model: %s", model.Name)
		}
		for _, id := range []string{model.Name, model.Alias} {
			if id == "" {
				continue
			}
			if prior, ok := seen[id]; ok && prior != model.Name {
				return auth, errors.New("ambiguous OpenCode model alias")
			}
			seen[id] = model.Name
		}
	}
	for name, value := range auth.Headers {
		if !validNativeHeader(name, value) {
			return auth, errors.New("invalid OpenCode custom header")
		}
	}
	return auth, nil
}

func validNativeHeader(name, value string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	for _, c := range value {
		if c < 32 && c != '\t' || c == 127 {
			return false
		}
	}
	return true
}

func nativeHostCall(method string, request, result any) error {
	hostCallMu.RLock()
	fn := hostCallFn
	hostCallMu.RUnlock()
	if fn == nil {
		return errors.New("host callback is unavailable")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	raw, err = fn(method, raw)
	if err != nil {
		return err
	}
	var envelope Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return errors.New(envelope.Error.Message)
		}
		return errors.New("host callback failed")
	}
	if result != nil && len(envelope.Result) > 0 {
		return json.Unmarshal(envelope.Result, result)
	}
	return nil
}

func (m *Manager) handleNative(method string, payload []byte) ([]byte, error) {
	switch method {
	case "auth.identifier", "executor.identifier":
		return OKEnvelope(map[string]string{"identifier": PluginID}), nil
	case "auth.parse":
		var req struct {
			Provider, Path, FileName string
			RawJSON                  []byte
			Host                     struct{ ProxyURL string }
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		var kind struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(req.RawJSON, &kind)
		if req.Provider != PluginID && kind.Type != PluginID {
			return OKEnvelope(map[string]any{"Handled": false}), nil
		}
		auth, err := m.decodeNativeAuth(req.RawJSON)
		if err != nil {
			return nativeError("invalid_auth", err.Error(), 400), nil
		}
		name := req.FileName
		if name == "" {
			name = filepath.Base(req.Path)
		}
		if name == "" || name == "." {
			return nativeError("invalid_auth", "OpenCode credential file name is required", 400), nil
		}
		// A base_url attribute selects CPA's built-in executor; keep it in StorageJSON.
		attrs := map[string]string{"api_key": auth.APIKey, "auth_type": "api_key"}
		if auth.ProxyURL == "" {
			auth.ProxyURL = normalizeNativeProxy(req.Host.ProxyURL)
		}
		attrs["proxy_url"] = auth.ProxyURL
		for key, value := range auth.Headers {
			attrs["header:"+key] = value
		}
		var metadata map[string]any
		_ = json.Unmarshal(req.RawJSON, &metadata)
		return OKEnvelope(map[string]any{"Handled": true, "Auth": nativeAuthData{Provider: PluginID, ID: PluginID + ":" + name, FileName: name, Label: auth.Label, Prefix: auth.Prefix, ProxyURL: auth.ProxyURL, Disabled: auth.Disabled, StorageJSON: req.RawJSON, Metadata: metadata, Attributes: attrs}}), nil
	case "model.static":
		return OKEnvelope(map[string]any{"Provider": PluginID, "Models": []any{}}), nil
	case "model.for_auth":
		var req struct {
			AuthProvider string
			StorageJSON  []byte
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		if req.AuthProvider != PluginID {
			return OKEnvelope(map[string]any{}), nil
		}
		auth, err := m.decodeNativeAuth(req.StorageJSON)
		if err != nil {
			return nativeError("invalid_auth", err.Error(), 400), nil
		}
		models := []map[string]any{}
		seen := map[string]bool{}
		for _, model := range auth.Models {
			for _, id := range []string{model.Name, model.Alias} {
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				models = append(models, map[string]any{"ID": id, "Name": model.Name, "Object": "model", "OwnedBy": "opencode", "Type": "openai", "DisplayName": id})
			}
		}
		return OKEnvelope(map[string]any{"Provider": PluginID, "Models": models}), nil
	case "model.route":
		var req struct{ RequestedModel string }
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		var result struct {
			Credentials []struct {
				Provider string   `json:"provider"`
				Models   []string `json:"models"`
			} `json:"credentials"`
		}
		if err := nativeHostCall("host.auth.models", struct{}{}, &result); err != nil {
			return OKEnvelope(map[string]any{"Handled": false}), nil
		}
		for _, auth := range result.Credentials {
			if auth.Provider != PluginID {
				continue
			}
			for _, model := range auth.Models {
				if req.RequestedModel == model {
					return OKEnvelope(map[string]any{"Handled": true, "TargetKind": "provider", "Target": PluginID, "TargetModel": model}), nil
				}
			}
		}
		return OKEnvelope(map[string]any{"Handled": false}), nil
	case "executor.execute", "executor.execute_stream":
		var req nativeExecutorRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		return m.executeNative(req, method == "executor.execute_stream"), nil
	case "request.complete":
		var req struct{ RequestID string }
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		m.mu.Lock()
		if execution := m.nativeRequests[req.RequestID]; execution != nil {
			execution.cancel()
			delete(m.nativeRequests, req.RequestID)
		}
		m.mu.Unlock()
		return OKEnvelope(struct{}{}), nil
	case "auth.refresh":
		var req struct {
			AuthID, AuthProvider string
			StorageJSON          []byte
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		return OKEnvelope(map[string]any{"Auth": nativeAuthData{Provider: req.AuthProvider, ID: req.AuthID, StorageJSON: req.StorageJSON}}), nil
	default:
		return nativeError("unsupported_operation", "OpenCode does not support this operation: "+method, http.StatusNotImplemented), nil
	}
}

// Caller holds m.mu; cancellation never calls back into the manager.
func (m *Manager) cancelNativeRequests() {
	for id, execution := range m.nativeRequests {
		execution.cancel()
		delete(m.nativeRequests, id)
	}
}

func (m *Manager) nativeContext(id string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	execution := &nativeExecution{cancel: cancel}
	m.mu.Lock()
	if prior := m.nativeRequests[id]; prior != nil {
		prior.cancel()
	}
	m.nativeRequests[id] = execution
	m.mu.Unlock()
	return ctx, func() {
		cancel()
		m.mu.Lock()
		if m.nativeRequests[id] == execution {
			delete(m.nativeRequests, id)
		}
		m.mu.Unlock()
	}
}
