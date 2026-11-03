// Package opencodefingerprint describes the OpenCode client request envelope.
// It does not authenticate, route requests, or execute the advertised tools.
package opencodefingerprint

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
)

const (
	Version         = "2.2.5"
	UserAgent       = "opencode/latest/" + Version + "/cli"
	Accept          = "application/json, text/event-stream"
	ToolDescription = "Not available in this session. Do not call this tool."
)

// Headers returns a fresh request fingerprint with a single conversation ID
// across all affinity headers and matching B3/W3C per-request tracing IDs.
func Headers(session, project string) http.Header {
	var trace [16]byte
	var span [8]byte
	var parent [8]byte
	_, _ = rand.Read(trace[:])
	_, _ = rand.Read(span[:])
	_, _ = rand.Read(parent[:])
	traceID, spanID := hex.EncodeToString(trace[:]), hex.EncodeToString(span[:])
	h := http.Header{}
	h.Set("User-Agent", UserAgent)
	h.Set("Accept", Accept)
	h.Set("X-Opencode-Client", "cli")
	h.Set("X-Opencode-Project", ProjectID(project))
	for _, name := range []string{"X-Opencode-Session", "X-Opencode-Session-Id", "X-Session-Affinity", "X-Session-Id"} {
		h.Set(name, session)
	}
	h.Set("B3", traceID+"-"+spanID+"-1-"+hex.EncodeToString(parent[:]))
	h.Set("Traceparent", "00-"+traceID+"-"+spanID+"-01")
	return h
}

// ProjectID preserves an existing lowercase SHA-1 identifier; other values use
// a stable namespace so raw project names and paths are not sent upstream.
func ProjectID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 40 && strings.IndexFunc(value, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') }) == -1 {
		return value
	}
	if value == "" {
		value = workingDirectory()
	}
	digest := sha1.Sum([]byte("opencode-project\x00" + value))
	return hex.EncodeToString(digest[:])
}

func workingDirectory() string {
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		return cwd
	}
	return "/workspace"
}

// Environment supplies real process context when the client supplied none.
func Environment() string {
	return "You are a coding agent.\n<env>\nWorking directory: " + workingDirectory() + "\n</env>"
}

func ToolNames() []string { return []string{"edit", "glob", "grep", "read", "shell"} }

// ToolSchema returns a fresh schema; callers can safely adapt protocol wrappers.
// These declarations describe unavailable compatibility tools, not executors.
func ToolSchema(name string) map[string]any {
	props := map[string]any{}
	var required []string
	add := func(kind string, names ...string) {
		for _, key := range names {
			props[key] = map[string]any{"type": kind}
		}
	}
	switch name {
	case "edit":
		add("string", "path", "oldString", "newString")
		add("boolean", "replaceAll")
		required = []string{"path", "oldString", "newString"}
	case "glob":
		add("string", "pattern", "path")
		add("boolean", "hidden")
		add("integer", "limit")
		required = []string{"pattern"}
	case "grep":
		add("string", "pattern", "path", "include")
		add("boolean", "literal", "caseSensitive")
		add("integer", "limit")
		required = []string{"pattern"}
	case "read":
		add("string", "path")
		add("integer", "offset", "limit")
		required = []string{"path"}
	case "shell":
		add("string", "command", "workdir")
		add("integer", "timeout")
		add("boolean", "background")
		required = []string{"command"}
	default:
		// Preserve the legacy generic schema for explicitly configured extras.
		add("string", "value")
		return map[string]any{"type": "object", "properties": props}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
