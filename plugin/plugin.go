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

	// 1. Session header. A native x-opencode-session (real OpenCode client)
	// is authoritative and is never overridden.
	if res, ok := resolveSessionID(req, cfg, cfg.Session.HeaderName); ok {
		logValues["session_source"] = res.Source
		if res.Source != "native" {
			value := sessionHeaderValue(res, cfg)
			resp.Headers.Set(cfg.Session.HeaderName, value)
			logValues["session"] = value
		} else {
			logValues["session"] = "native (preserved)"
		}
	}

	// 2. Client identity + user-agent rewrite.
	if BoolVal(cfg.UserAgent.Rewrite, DefaultRewriteUA) {
		clientType := detectClientType(req.Headers)
		clientUA := headerGet(req.Headers, "User-Agent")
		logValues["client_type"] = clientType
		if clientUA != "" {
			logValues["client_ua"] = clientUA
		}
		if ua := resolveUserAgent(cfg, clientType, clientUA); ua != "" {
			outbound := strings.TrimSpace(cfg.UserAgent.OutboundHeader)
			if outbound == "" {
				outbound = DefaultOutboundUAHeader
			}
			resp.Headers.Set(outbound, ua)
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

	// 3. Zen free-tier body cleanup: drop rejected input[] entry types.
	bodyChanged := false
	if BoolVal(cfg.BodyClean.StripAdditionalTools, DefaultStripTools) {
		if isZenFreeTierModel(req.Model, cfg.BodyClean) ||
			isZenFreeTierModel(req.RequestedModel, cfg.BodyClean) {
			if fixed, changed := stripInputTypes(req.Body, cfg.BodyClean.StripTypes); changed {
				resp.Body = fixed
				bodyChanged = true
			}
		}
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
