package plugin

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// captureHostLog installs a host caller that records host.log messages and
// restores the previous logging state when the test ends.
func captureHostLog(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	SetHostCaller(func(method string, payload []byte) ([]byte, error) {
		if method != MethodHostLog {
			return nil, nil
		}
		var entry struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(payload, &entry); err == nil {
			mu.Lock()
			got = append(got, entry.Level+" "+entry.Message)
			mu.Unlock()
		}
		return nil, nil
	})
	t.Cleanup(func() { SetHostCaller(nil); SetLogEnabled(false) })
	return &got
}

// TestGlueWarningFiresWithLoggingDisabled is the point of the warning: the
// operator has not enabled logging (the default), the credential is missing
// its headers: glue, and upstream answers 403 with nothing in the log to
// explain it. The warning must get through anyway.
func TestGlueWarningFiresWithLoggingDisabled(t *testing.T) {
	got := captureHostLog(t)
	SetLogEnabled(false)

	m := NewManager()
	m.warnGlueRequirement("openai-compatible-opencode zen", DefaultConfig())

	if len(*got) != 1 {
		t.Fatalf("got %d log lines, want 1: %v", len(*got), *got)
	}
	line := (*got)[0]
	if !strings.HasPrefix(line, "warn ") {
		t.Errorf("warning logged at the wrong level: %q", line)
	}
	for _, want := range []string{
		"openai-compatible-opencode zen",
		`User-Agent: "$X-Opencode-User-Agent"`,
		`X-Opencode-Session: "$X-Opencode-Session"`,
		`X-Opencode-Client: "$X-Opencode-Client"`,
		`X-Opencode-Project: "$X-Opencode-Project"`,
		`X-Opencode-Request: "$X-Opencode-Request"`,
		`Accept: "$Accept"`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("warning does not name %q:\n%s", want, line)
		}
	}
}

// TestGlueWarningIsOncePerAuth keeps the warning from becoming per-request
// noise, while still covering a second credential.
func TestGlueWarningIsOncePerAuth(t *testing.T) {
	got := captureHostLog(t)
	cfg := DefaultConfig()
	m := NewManager()

	for i := 0; i < 3; i++ {
		m.warnGlueRequirement("auth-a", cfg)
	}
	m.warnGlueRequirement("auth-b", cfg)

	if len(*got) != 2 {
		t.Fatalf("got %d log lines, want 2 (one per auth): %v", len(*got), *got)
	}
}

func TestGlueWarningCanBeSilenced(t *testing.T) {
	got := captureHostLog(t)
	cfg := DefaultConfig()
	off := false
	cfg.Fingerprint.WarnMissingGlue = &off

	NewManager().warnGlueRequirement("auth-a", cfg)

	if len(*got) != 0 {
		t.Fatalf("warn_missing_glue: false still logged: %v", *got)
	}
}

// TestRequiredGlueHeadersFollowConfig guards the case where an operator
// renamed the outbound UA or session header: the warning must name what they
// actually have to declare, not the defaults.
func TestRequiredGlueHeadersFollowConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UserAgent.OutboundHeader = "X-Custom-UA"
	cfg.Session.HeaderName = "x-my-session"
	cfg.Fingerprint.Accept = ""

	glue := strings.Join(requiredGlueHeaders(cfg), ", ")
	for _, want := range []string{`User-Agent: "$X-Custom-UA"`, `X-My-Session: "$X-My-Session"`} {
		if !strings.Contains(glue, want) {
			t.Errorf("glue does not name %q: %s", want, glue)
		}
	}
	if strings.Contains(glue, "Accept") {
		t.Errorf("Accept listed although fingerprint.accept is empty: %s", glue)
	}
}
