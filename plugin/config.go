// Package plugin implements the CLIProxyAPI method dispatcher for the
// opencode-enhancer interceptor plugin: session header injection,
// user-agent rewrite, and zen free-tier body cleanup.
package plugin

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ABIVersion matches the host's expected C ABI version.
const ABIVersion uint32 = 1

// Plugin identity constants.
const (
	PluginID      = "opencode-enhancer"
	PluginVersion = "0.4.3"
	GitHubRepo    = "https://github.com/YogaSakti/cpa-opencode-enhancer"
)

// Defaults.
const (
	DefaultSessionHeaderName = "x-opencode-session"
	DefaultHashDerived       = true
	DefaultFallbackBodyHash  = true
	// DefaultFallbackRequestID is OFF: a request id is never a conversation
	// id, so forwarding it creates a fresh upstream session per request and
	// kills prompt-cache affinity. Only enable it to dodge a hard
	// MissingSessionID 400 when the body cannot be hashed either.
	DefaultFallbackRequestID = false
	DefaultRewriteUA         = true
	DefaultStripTools        = true
	// DefaultLogEnabled keeps host.log observability OFF unless explicitly
	// enabled in config: one log line per request is too noisy by default.
	DefaultLogEnabled = false
)

// Config is the plugin's configuration, loaded from plugins.configs.opencode-enhancer.
type Config struct {
	Session     SessionConfig     `yaml:"session"`
	UserAgent   UserAgentConfig   `yaml:"user_agent"`
	BodyClean   BodyCleanConfig   `yaml:"body_cleanup"`
	Fingerprint FingerprintConfig `yaml:"fingerprint"`
	Target      TargetConfig      `yaml:"target"`
	Logging     LoggingConfig     `yaml:"logging"`
}

// LoggingConfig controls host.log observability. Disabled by default: the
// per-request "shaped" lines are verbose and only useful while debugging.
type LoggingConfig struct {
	Enabled *bool `yaml:"enabled"`
}

// SessionConfig controls session header injection.
type SessionConfig struct {
	HeaderName        string   `yaml:"header_name"`
	SourceHeaders     []string `yaml:"source_headers"`
	HashDerived       *bool    `yaml:"hash_derived"`
	FallbackBodyHash  *bool    `yaml:"fallback_to_body_hash"`
	FallbackRequestID *bool    `yaml:"fallback_to_request_id"`
}

// UserAgentConfig controls user-agent rewrite.
type UserAgentConfig struct {
	Rewrite            *bool             `yaml:"rewrite"`
	Mode               string            `yaml:"mode"`
	Value              string            `yaml:"value"`
	Template           string            `yaml:"template"`
	FallbackValue      string            `yaml:"fallback_value"`
	CustomUA           map[string]string `yaml:"custom_ua"`
	GenericPatterns    []string          `yaml:"generic_patterns"`
	ClientMap          map[string]string `yaml:"client_map"`
	OutboundHeader     string            `yaml:"outbound_header"`
	SetUserAgentHeader bool              `yaml:"set_user_agent_header"`
}

// BodyCleanConfig controls zen free-tier body cleanup.
type BodyCleanConfig struct {
	StripAdditionalTools *bool    `yaml:"strip_additional_tools"`
	FreeMarkers          []string `yaml:"free_markers"`
	ZenFreeModels        []string `yaml:"zen_free_models"`
	ZenPaidModels        []string `yaml:"zen_paid_models"`
	StripTypes           []string `yaml:"strip_types"`
}

// TargetConfig controls which requests are intercepted.
type TargetConfig struct {
	BaseURLMarkers []string `yaml:"base_url_markers"`
	AuthPrefixes   []string `yaml:"auth_prefixes"`
	Models         []string `yaml:"models"`
}

// DefaultConfig returns a fully populated default configuration.
func DefaultConfig() Config {
	hashDerived := DefaultHashDerived
	fallbackBody := DefaultFallbackBodyHash
	fallbackReqID := DefaultFallbackRequestID
	rewriteUA := DefaultRewriteUA
	stripTools := DefaultStripTools
	logEnabled := DefaultLogEnabled

	return Config{
		Session: SessionConfig{
			HeaderName: DefaultSessionHeaderName,
			SourceHeaders: []string{
				"X-Opencode-Session",
				"Session-Id",
				"Session_id",
				"Thread-Id",
				"Thread_id",
				"X-Claude-Code-Session-Id",
				"X-DeepSeek-Harness-Session-Id",
				// X-Session-Affinity ranks above X-Session-Id: it is set only by
				// dsh's pi-ai transport and is conversation-specific, while the
				// generic X-Session-Id may be stamped per call by other tools.
				"X-Session-Affinity",
				"X-Session-Id",
				// Deliberately EXCLUDED from the defaults: X-Conversation-Id /
				// X-Thread-Id are proxy/server stamps (not client conversation
				// ids) and may vary per request, which would override the
				// stable body-hash fallback with an unstable session. Add them
				// back only when the client genuinely sends stable values.
				//
				// Last resort: dsh pi-ai openai-responses stamps the session
				// id here (alongside x-session-affinity/x-session-id, which
				// those clients may not use). Ranked last because other
				// tools set it per call.
				"X-Client-Request-Id",
			},
			HashDerived:       &hashDerived,
			FallbackBodyHash:  &fallbackBody,
			FallbackRequestID: &fallbackReqID,
		},
		UserAgent: UserAgentConfig{
			Rewrite:         &rewriteUA,
			Mode:            DefaultUAMode,
			FallbackValue:   DefaultFallbackUA,
			CustomUA:        map[string]string{},
			GenericPatterns: []string{},
			ClientMap: map[string]string{
				"codex":    "codex",
				"claude":   "claude-code",
				"opencode": "opencode",
				// No value for "generic": unidentified clients get no
				// X-Opencode-Client header at all (omitting beats sending a
				// self-identifying proxy label).
				"generic": "",
			},
			OutboundHeader:     DefaultOutboundUAHeader,
			SetUserAgentHeader: false,
		},
		BodyClean: BodyCleanConfig{
			StripAdditionalTools: &stripTools,
			FreeMarkers:          []string{"-free", ":free"},
			ZenFreeModels:        []string{},
			ZenPaidModels:        []string{},
			StripTypes:           []string{"additional_tools"},
		},
		Fingerprint: defaultFingerprintConfig(),
		Target: TargetConfig{
			BaseURLMarkers: []string{"opencode.ai"},
			// Matched as a case-insensitive substring of the host's auth id.
			// The host builds that id from the credential's display name
			// ("Opencode Zen" -> "openai-compatible-opencode zen"), so a bare
			// "opencode" covers every sensible naming, including the older
			// "openai-compatibility:opencode:" form.
			AuthPrefixes: []string{"opencode"},
			Models:       []string{},
		},
		Logging: LoggingConfig{
			Enabled: &logEnabled,
		},
	}
}

// LoadConfig decodes YAML config bytes into a Config, applying defaults.
func LoadConfig(yamlBytes []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(yamlBytes) == 0 {
		return cfg, nil
	}
	if err := yaml.Unmarshal(yamlBytes, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	return cfg, nil
}

// BoolVal returns the value of a *bool or the fallback.
func BoolVal(p *bool, fallback bool) bool {
	if p != nil {
		return *p
	}
	return fallback
}
