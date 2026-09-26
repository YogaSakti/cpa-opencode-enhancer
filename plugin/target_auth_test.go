package plugin

import "testing"

// TestIsTargetMatchesHostDerivedAuthIDs pins the auth ids CLIProxyAPI actually
// produces. It derives them from the credential's display name, so the ids
// below are what a live host sent; a prefix match against
// "openai-compatibility:opencode:" missed all of them and the plugin silently
// skipped every request.
func TestIsTargetMatchesHostDerivedAuthIDs(t *testing.T) {
	cfg := DefaultConfig()
	cases := []struct {
		authID string
		want   bool
	}{
		{"openai-compatible-opencode zen", true},  // live host, "Opencode Zen"
		{"openai-compatible-opencode go", true},   // live host, "Opencode Go"
		{"openai-compatibility:opencode:1", true}, // older form, still matches
		{"OPENAI-COMPATIBLE-OpenCode Zen", true},  // case must not matter
		{"openai-compatible-openrouter", false},   // unrelated provider
		{"", false},
	}
	for _, tc := range cases {
		req := RequestInterceptRequest{
			Model:    "mimo-v2.5-free",
			Metadata: map[string]any{"selected_auth_id": tc.authID},
		}
		if got := isTarget(req, cfg); got != tc.want {
			t.Errorf("isTarget(auth=%q) = %v, want %v", tc.authID, got, tc.want)
		}
	}
}

// TestIsTargetCodexAPIKeyNeedsModelGlob guards the Muse transport: the host
// passes no base URL to interceptors and a codex-api-key auth id carries no
// provider name, so only a target.models glob selects it. Without one the
// request goes out unshaped and upstream answers 403 FreeTierError.
func TestIsTargetCodexAPIKeyNeedsModelGlob(t *testing.T) {
	cfg := DefaultConfig()
	req := RequestInterceptRequest{
		Model:    "muse-spark-1.3-contributor-free",
		Metadata: map[string]any{"selected_auth_id": "codex:apikey:5faa5798d3a4"},
	}
	if isTarget(req, cfg) {
		t.Fatal("default config matched a codex-api-key auth id")
	}
	cfg.Target.Models = []string{"muse-*"}
	if !isTarget(req, cfg) {
		t.Fatal("target.models glob did not select the codex-api-key credential")
	}
}

// TestIsTargetIgnoresBlankMarkers stops an empty config entry from turning the
// plugin into a match-everything interceptor.
func TestIsTargetIgnoresBlankMarkers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Target.AuthPrefixes = []string{"", "   "}
	req := RequestInterceptRequest{
		Model:    "mimo-v2.5-free",
		Metadata: map[string]any{"selected_auth_id": "openai-compatible-openrouter"},
	}
	if isTarget(req, cfg) {
		t.Fatal("a blank marker matched an unrelated auth id")
	}
}
