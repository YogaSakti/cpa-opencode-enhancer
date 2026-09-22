package plugin

import (
	"net/http"
	"path"
	"strings"
)

// isTarget checks whether the request should be intercepted based on
// the configured target detection rules.
func isTarget(req RequestInterceptRequest, cfg Config) bool {
	// Check model globs first (most specific).
	for _, model := range []string{req.Model, req.RequestedModel} {
		for _, pattern := range cfg.Target.Models {
			if matched, _ := path.Match(pattern, model); matched {
				return true
			}
		}
	}

	// Check the auth id markers. Matched as a substring, not a prefix: the
	// host derives the id from the provider's display name, so a credential
	// called "Opencode Zen" arrives as "openai-compatible-opencode zen" and a
	// prefix match silently misses every request. Substring matching is a
	// superset of the old prefix behaviour, so existing configs keep working.
	selectedAuth := strings.ToLower(metadataString(req.Metadata, "selected_auth_id"))
	selectedIndex := strings.ToLower(metadataString(req.Metadata, "selected_auth_index"))
	for _, marker := range cfg.Target.AuthPrefixes {
		marker = strings.ToLower(strings.TrimSpace(marker))
		if marker == "" {
			continue
		}
		if strings.Contains(selectedAuth, marker) || strings.Contains(selectedIndex, marker) {
			return true
		}
	}

	// Check base URL markers (from metadata or headers).
	baseURL := metadataString(req.Metadata, "base_url")
	if baseURL == "" {
		// Fallback: check headers for base-url hint.
		baseURL = req.Headers.Get("X-CPA-Base-URL")
	}
	baseURL = strings.ToLower(baseURL)
	for _, marker := range cfg.Target.BaseURLMarkers {
		if marker != "" && strings.Contains(baseURL, strings.ToLower(strings.TrimSpace(marker))) {
			return true
		}
	}

	return false
}

// isZenFreeTierModel reports whether a model is a free-tier variant, using a
// dynamic marker match rather than a hardcoded model list:
//
//  1. an explicit paid_models entry always wins — a guard for paid builds
//     that happen to carry a marker;
//  2. an explicit free_models entry is treated as free (escape hatch for
//     free models with no marker);
//  3. otherwise the model's final segment is checked for any markers
//     suffix (default "-free" and ":free").
//
// Provider prefixes ("opencode-go/muse-free") are stripped before matching,
// and the comparison is case-insensitive.
func isZenFreeTierModel(model string, cfg FreeTierConfig) bool {
	m := lastSegment(model)
	if m == "" {
		return false
	}
	if isExplicitPaidModel(m, cfg) {
		return false
	}
	for _, free := range cfg.FreeModels {
		if f := lastSegment(free); f != "" && m == f {
			return true
		}
	}
	for _, marker := range cfg.Markers {
		mk := strings.ToLower(strings.TrimSpace(marker))
		if mk != "" && strings.HasSuffix(m, mk) {
			return true
		}
	}
	return false
}

// isZenGoMuseContributor reports whether the request targets Muse Contributor
// on OpenCode Go. Go serves these builds from its free tier without a -free
// suffix, so model-marker classification alone cannot recognize them.
func isZenGoMuseContributor(req RequestInterceptRequest, cfg FreeTierConfig) bool {
	baseURL := strings.ToLower(metadataString(req.Metadata, "base_url"))
	if baseURL == "" {
		baseURL = strings.ToLower(req.Headers.Get("X-CPA-Base-URL"))
	}
	selectedAuth := strings.ToLower(metadataString(req.Metadata, "selected_auth_id"))
	selectedIndex := strings.ToLower(metadataString(req.Metadata, "selected_auth_index"))
	if !strings.Contains(baseURL, "/zen/go/") &&
		!strings.Contains(selectedAuth, "opencode go") &&
		!strings.Contains(selectedIndex, "opencode go") {
		return false
	}

	for _, model := range []string{req.Model, req.RequestedModel} {
		m := lastSegment(model)
		if !isExplicitPaidModel(m, cfg) && strings.HasPrefix(m, "muse-spark-") && strings.HasSuffix(m, "-contributor") {
			return true
		}
	}
	return false
}

func isExplicitPaidModel(model string, cfg FreeTierConfig) bool {
	for _, paid := range cfg.PaidModels {
		if p := lastSegment(paid); p != "" && model == p {
			return true
		}
	}
	return false
}

// lastSegment lowercases a model id and returns its final "/"-separated part.
func lastSegment(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.TrimSpace(m)
}

// metadataString extracts a string value from metadata map.
func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	v, _ := metadata[key].(string)
	return strings.TrimSpace(v)
}

// headerValue does case-insensitive header lookup.
func headerValue(headers http.Header, name string) (string, bool) {
	if headers == nil {
		return "", false
	}
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			if v := strings.TrimSpace(values[0]); v != "" {
				return v, true
			}
		}
	}
	return "", false
}

// hasHeader checks if a header exists (case-insensitive).
func hasHeader(headers http.Header, name string) bool {
	_, ok := headerValue(headers, name)
	return ok
}
