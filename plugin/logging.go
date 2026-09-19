package plugin

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// hostCaller invokes a host RPC method from plugin code. Wired by the CGO
// entrypoint (main.go) during cliproxy_plugin_init.
type hostCaller func(method string, payload []byte) ([]byte, error)

var (
	hostCallMu     sync.RWMutex
	hostCallFn     hostCaller
	hostLogEnabled = true
)

// MethodHostLog is the host RPC that writes to the CLIProxyAPI log.
const MethodHostLog = "host.log"

// SetHostCaller installs the host RPC bridge. Called once at plugin init.
func SetHostCaller(fn hostCaller) {
	hostCallMu.Lock()
	hostCallFn = fn
	hostCallMu.Unlock()
}

// SetLogEnabled toggles host logging (used by tests and config).
func SetLogEnabled(enabled bool) {
	hostCallMu.Lock()
	hostLogEnabled = enabled
	hostCallMu.Unlock()
}

// applyLoggingConfig enables host.log observability only when the operator
// explicitly opted in (logging.enabled: true). Default: off.
func applyLoggingConfig(cfg Config) {
	SetLogEnabled(BoolVal(cfg.Logging.Enabled, DefaultLogEnabled))
}

// logToHost writes a line to the CLIProxyAPI log via host.log.
// Failures are swallowed: observability must never break request handling.
//
// The host's LogFormatter only renders a whitelist of structured field keys
// (logFieldOrder in internal/logging/global_logger.go); anything else is
// dropped silently. Only whitelisted keys are sent as fields, and the full
// detail goes into the message, which is always printed.
func logToHost(level, message string, fields map[string]any) {
	hostCallMu.RLock()
	enabled := hostLogEnabled
	hostCallMu.RUnlock()
	if !enabled {
		return
	}
	logToHostAlways(level, message, fields)
}

// logToHostAlways writes to the CLIProxyAPI log regardless of
// logging.enabled. Reserved for setup errors the operator cannot diagnose
// any other way: a misconfigured install is otherwise completely silent —
// upstream answers 403 and nothing in the log points at the cause.
func logToHostAlways(level, message string, fields map[string]any) {
	hostCallMu.RLock()
	fn := hostCallFn
	hostCallMu.RUnlock()
	if fn == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"level":   level,
		"message": message,
		"fields":  fields,
	})
	if err != nil {
		return
	}
	if _, err := fn(MethodHostLog, payload); err != nil {
		_ = err // best effort only
	}
}

// kv renders key=value pairs in a stable order for the log message.
func kv(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := strings.TrimSpace(values[k])
		if v == "" {
			continue
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " ")
}

// logIntercept reports what the plugin changed for one request.
func logIntercept(req RequestInterceptRequest, values map[string]string, bodyChanged bool) {
	details := map[string]string{}
	for k, v := range values {
		details[k] = v
	}
	if details["session"] == "" {
		details["session"] = "(none)"
	}
	details["body_cleanup"] = fmt.Sprint(bodyChanged)
	details["auth"] = metadataString(req.Metadata, "selected_auth_id")

	logToHost(
		"info",
		"opencode-enhancer: shaped "+kv(details),
		map[string]any{"model": strings.TrimSpace(req.Model)},
	)
}

// logSkip reports that a request was not targeted (debug level).
func logSkip(req RequestInterceptRequest) {
	logToHost(
		"debug",
		"opencode-enhancer: not targeted, passthrough",
		map[string]any{"model": strings.TrimSpace(req.Model)},
	)
}
