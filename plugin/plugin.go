// Package plugin implements the CLIProxyAPI method dispatcher for the
// opencode-enhancer interceptor plugin.
package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// RPC method names (pluginabi constants, kept local to stay SDK-free).
const (
	MethodPluginRegister         = "plugin.register"
	MethodPluginReconfigure      = "plugin.reconfigure"
	MethodPluginQuiesce          = "plugin.quiesce"
	MethodPluginShutdown         = "plugin.shutdown"
	MethodRequestInterceptBefore = "request.intercept_before"
	MethodRequestInterceptAfter  = "request.intercept_after"
	MethodSchedulerPick          = "scheduler.pick"
)

// Manager owns the plugin state and routes every RPC method. Safe for
// concurrent HandleCall use.
type Manager struct {
	mu sync.RWMutex

	cfg Config
	// knownAuths holds auth IDs observed by the scheduler hook whose
	// provider base URL matched a configured marker. Bounded by the
	// number of credentials; reset on reconfigure.
	knownAuths map[string]struct{}
}

// NewManager returns a dispatcher with default configuration.
func NewManager() *Manager {
	return &Manager{cfg: DefaultConfig(), knownAuths: make(map[string]struct{})}
}

// HandleCall dispatches one RPC method and returns envelope bytes.
func (m *Manager) HandleCall(method string, payload []byte) ([]byte, error) {
	switch method {
	case MethodPluginRegister, MethodPluginReconfigure:
		return m.handleLifecycle(payload)
	case MethodRequestInterceptBefore:
		// The before-auth path REPLACES headers when a non-empty map is
		// returned (finalInterceptorHeaders), so it must stay a no-op.
		// All shaping happens in intercept_after, which merges.
		return emptyInterceptResponse(), nil
	case MethodRequestInterceptAfter:
		return m.handleInterceptAfter(payload)
	case MethodSchedulerPick:
		return m.handleSchedulerPick(payload)
	case MethodPluginShutdown, MethodPluginQuiesce:
		m.mu.Lock()
		m.cfg = DefaultConfig()
		m.knownAuths = make(map[string]struct{})
		m.mu.Unlock()
		SetLogEnabled(false)
		return OKEnvelope(struct{}{}), nil
	default:
		return ErrEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// handleLifecycle implements plugin.register / plugin.reconfigure.
func (m *Manager) handleLifecycle(payload []byte) ([]byte, error) {
	var req LifecycleRequest
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
	}
	cfg, err := LoadConfig(req.ConfigYAML)
	if err != nil {
		return ErrEnvelope("invalid_config", err.Error()), nil
	}
	if err := validateConfig(cfg); err != nil {
		return ErrEnvelope("invalid_config", err.Error()), nil
	}
	m.mu.Lock()
	m.cfg = cfg
	m.knownAuths = make(map[string]struct{})
	m.mu.Unlock()
	applyLoggingConfig(cfg)
	return OKEnvelope(registration()), nil
}

// handleInterceptAfter implements request.intercept_after: session header
// injection, client identity / user-agent rewrite, and zen free-tier body
// cleanup. The host merges returned headers into the execution headers.
func (m *Manager) handleInterceptAfter(payload []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
	}

	m.mu.RLock()
	cfg := m.cfg
	known := m.knownAuths
	m.mu.RUnlock()

	if !isTarget(req, cfg) &&
		!isKnownOpenCodeAuth(metadataString(req.Metadata, "selected_auth_id"), known) {
		logSkip(req)
		return emptyInterceptResponse(), nil
	}

	resp := RequestInterceptResponse{Headers: http.Header{}}
	logValues := map[string]string{}

	// The Zen free tier gates on the *whole* official-client fingerprint:
	// UA, client/project/session/request headers, streaming, and the tool
	// quartet. Missing any one of them returns 403 FreeTierError.
	fingerprint := fingerprintApplies(req.Model, req.RequestedModel, cfg)
	logValues["fingerprint"] = boolLabel(fingerprint)

	// 1. Session header. Outside the fingerprint path a native
	// x-opencode-session (real OpenCode client) is authoritative and is never
	// overridden; on the fingerprint path a value that does not already carry
	// the official ses_ shape is reshaped, because upstream rejects anything
	// else.
	res, haveSession := resolveSessionID(req, cfg, cfg.Session.HeaderName)
	if !haveSession && fingerprint && req.RequestID != "" {
		// Last resort: upstream needs *some* ses_ id. Not sticky, but a
		// per-request session beats a hard 403.
		res = sessionResult{Value: req.RequestID, Source: "request", Stable: false}
		haveSession = true
	}
	if haveSession {
		logValues["session_source"] = res.Source
		switch {
		case fingerprint:
			value := res.Value
			if !isOpenCodeSessionID(value) {
				value = shapeSessionID(value)
			}
			resp.Headers.Set(cfg.Session.HeaderName, value)
			logValues["session"] = value
		case res.Source != "native":
			value := sessionHeaderValue(res, cfg)
			resp.Headers.Set(cfg.Session.HeaderName, value)
			logValues["session"] = value
		default:
			logValues["session"] = "native (preserved)"
		}
	}

	// 2. Client identity + user-agent. On the fingerprint path both are fixed
	// to the official client's values; the client's own UA would fail the gate.
	clientType := detectClientType(req.Headers)
	clientUA := headerGet(req.Headers, "User-Agent")
	logValues["client_type"] = clientType
	if clientUA != "" {
		logValues["client_ua"] = clientUA
	}
	switch {
	case fingerprint:
		ua := fingerprintValue(cfg.Fingerprint.UserAgent, DefaultFingerprintUA)
		resp.Headers.Set(outboundUAHeader(cfg), ua)
		resp.Headers.Set("User-Agent", ua)
		resp.Headers.Set(DefaultIdentityHeader,
			fingerprintValue(cfg.Fingerprint.Client, DefaultFingerprintClient))
		resp.Headers.Set(HeaderOpenCodeProject,
			fingerprintValue(cfg.Fingerprint.Project, DefaultFingerprintProject))
		resp.Headers.Set(HeaderOpenCodeRequest, newRequestID())
		if accept := strings.TrimSpace(cfg.Fingerprint.Accept); accept != "" {
			resp.Headers.Set("Accept", accept)
		}
		logValues["outbound_ua"] = ua
		logValues["identity"] = fingerprintValue(cfg.Fingerprint.Client, DefaultFingerprintClient)
	case BoolVal(cfg.UserAgent.Rewrite, DefaultRewriteUA):
		if ua := resolveUserAgent(cfg, clientType, clientUA); ua != "" {
			resp.Headers.Set(outboundUAHeader(cfg), ua)
			logValues["outbound_ua"] = ua
			if cfg.UserAgent.SetUserAgentHeader {
				resp.Headers.Set("User-Agent", ua)
			}
		}
		if identity := resolveClientIdentity(cfg, clientType); identity != "" {
			resp.Headers.Set(DefaultIdentityHeader, identity)
			logValues["identity"] = identity
		}
	}

	// 3. Body shaping. Cleanup first (drop rejected input[] entry types), then
	// the fingerprint rewrite (stream, tool quartet, Responses hygiene).
	bodyChanged := false
	body := req.Body
	if BoolVal(cfg.BodyClean.StripAdditionalTools, DefaultStripTools) {
		if isZenFreeTierModel(req.Model, cfg.BodyClean) ||
			isZenFreeTierModel(req.RequestedModel, cfg.BodyClean) {
			if fixed, changed := stripInputTypes(body, cfg.BodyClean.StripTypes); changed {
				body = fixed
				bodyChanged = true
			}
		}
	}
	if fingerprint {
		if fixed, changed := applyFingerprintBody(body, cfg.Fingerprint); changed {
			body = fixed
			bodyChanged = true
		}
	}
	if bodyChanged {
		resp.Body = body
	}

	logIntercept(req, logValues, bodyChanged)

	if len(resp.Headers) == 0 && len(resp.Body) == 0 {
		return emptyInterceptResponse(), nil
	}
	return OKEnvelope(resp), nil
}

// handleSchedulerPick observes auth candidates and marks those whose
// provider base URL contains a configured marker. It never makes a
// scheduling decision itself (Handled: false delegates to the built-in).
func (m *Manager) handleSchedulerPick(payload []byte) ([]byte, error) {
	var req SchedulerPickRequest
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
	}
	m.mu.RLock()
	markers := m.cfg.Target.BaseURLMarkers
	m.mu.RUnlock()

	m.mu.Lock()
	for _, c := range req.Candidates {
		if candidateHasMarker(c, markers) {
			m.knownAuths[c.ID] = struct{}{}
		}
	}
	m.mu.Unlock()

	return OKEnvelope(SchedulerPickResponse{Handled: false}), nil
}

// candidateHasMarker reports whether a scheduler candidate's base URL
// contains any configured marker (case-insensitive).
func candidateHasMarker(c SchedulerAuthCandidate, markers []string) bool {
	baseURL := strings.TrimSpace(c.Attributes["base_url"])
	if baseURL == "" {
		if v, ok := c.Metadata["base_url"].(string); ok {
			baseURL = strings.TrimSpace(v)
		}
	}
	baseURL = strings.ToLower(baseURL)
	for _, marker := range markers {
		if marker != "" && strings.Contains(baseURL, strings.ToLower(strings.TrimSpace(marker))) {
			return true
		}
	}
	return false
}

// registration returns the plugin.register result.
func registration() Registration {
	return Registration{
		SchemaVersion: 1,
		Metadata: Metadata{
			Name:             PluginID,
			Version:          PluginVersion,
			Author:           "YogaSakti",
			GitHubRepository: GitHubRepo,
			ConfigFields: []ConfigField{
				{Name: "session.header_name", Type: "string", Description: "Header injected with the resolved OpenCode session id."},
				{Name: "session.hash_derived", Type: "boolean", Description: "Hash derived session identities before sending them upstream."},
				{Name: "user_agent.rewrite", Type: "boolean", Description: "Rewrite the outbound user-agent per detected client type."},
				{Name: "body_cleanup.strip_additional_tools", Type: "boolean", Description: "Strip additional_tools input entries for zen free-tier models."},
				{Name: "fingerprint.enabled", Type: "boolean", Description: "Send the official OpenCode client fingerprint required by the zen free tier."},
				{Name: "fingerprint.user_agent", Type: "string", Description: "User-Agent used on the fingerprint path (must be opencode/>=1.17)."},
				{Name: "fingerprint.force_stream", Type: "boolean", Description: "Force stream:true; the zen free tier rejects non-streaming requests."},
				{Name: "fingerprint.free_only", Type: "boolean", Description: "Restrict the fingerprint to free-tier models; paid builds are untouched."},
				{Name: "target.base_url_markers", Type: "array", Description: "Provider base URL markers that enable shaping for all models on those providers."},
			},
		},
		Capabilities: Capabilities{
			RequestInterceptor: true,
			Scheduler:          true,
		},
	}
}

// validateConfig performs light validation on the loaded config.
func validateConfig(cfg Config) error {
	names := []string{
		cfg.Session.HeaderName,
		cfg.UserAgent.OutboundHeader,
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.ContainsAny(name, "\r\n:") {
			return fmt.Errorf("header name contains invalid characters: %s", name)
		}
	}
	if len(cfg.Target.BaseURLMarkers) == 0 && len(cfg.Target.AuthPrefixes) == 0 && len(cfg.Target.Models) == 0 {
		return fmt.Errorf("at least one of target.base_url_markers, target.auth_prefixes, or target.models is required")
	}
	return nil
}
