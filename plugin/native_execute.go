package plugin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (m *Manager) executeNative(req nativeExecutorRequest, stream bool) []byte {
	if req.AuthProvider != PluginID {
		return nativeError("invalid_auth", "selected credential is not an OpenCode plugin auth", 400)
	}
	auth, err := m.decodeNativeAuth(req.StorageJSON)
	if err != nil {
		return nativeError("invalid_auth", err.Error(), 400)
	}
	if auth.ProxyURL == "" {
		auth.ProxyURL = req.AuthAttributes["proxy_url"]
	}
	model := ""
	requested := strings.TrimPrefix(req.Model, strings.Trim(auth.Prefix, "/")+"/")
	for _, candidate := range auth.Models {
		if requested == candidate.Name || (candidate.Alias != "" && requested == candidate.Alias) {
			model = candidate.Name
			break
		}
	}
	if model == "" {
		return nativeError("invalid_model", "model is not selected for this OpenCode credential", 400)
	}
	if stream && req.StreamID == "" {
		return nativeError("invalid_request", "stream_id is required", 400)
	}
	var root map[string]any
	if err = json.Unmarshal(req.Payload, &root); err != nil || root == nil {
		return nativeError("invalid_request", "invalid Chat Completions request body", 400)
	}
	root["model"] = model
	root["stream"] = true
	body, err := json.Marshal(root)
	if err != nil {
		return nativeError("invalid_request", "invalid Chat Completions request body", 400)
	}
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	body, _ = applyFingerprintBody(body, cfg.Fingerprint, "openai", "openai")
	headers := http.Header{}
	for name, value := range auth.Headers {
		if strings.HasPrefix(value, "$") {
			value = headerGet(req.Headers, strings.TrimPrefix(value, "$"))
			if value == "" {
				continue
			}
		}
		headers.Set(name, value)
	}
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "text/event-stream")
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(auth.APIKey))
	headers.Set("User-Agent", fingerprintValue(cfg.Fingerprint.UserAgent, DefaultFingerprintUA))
	headers.Set(DefaultIdentityHeader, fingerprintValue(cfg.Fingerprint.Client, DefaultFingerprintClient))
	headers.Set(HeaderOpenCodeProject, fingerprintValue(cfg.Fingerprint.Project, DefaultFingerprintProject))
	identity, ok := resolveSessionID(RequestInterceptRequest{SourceFormat: "openai", Body: req.Payload, Headers: req.Headers, Metadata: req.Metadata}, cfg, cfg.Session.HeaderName)
	if !ok {
		identity.Value = newRequestID()
	}
	if !isOpenCodeSessionID(identity.Value) {
		identity.Value = m.shapeSessionID(identity.Value)
	}
	headers.Set(cfg.Session.HeaderName, identity.Value)
	headers.Set(HeaderOpenCodeSessionID, identity.Value)
	headers.Set(HeaderOpenCodeRequest, newRequestID())
	headers.Del(nativeExecutionIDHeader)
	headers.Del(nativeSourceFormatHeader)
	for _, name := range []string{"Host", "Content-Length", "Transfer-Encoding", "Proxy-Authorization", "Proxy-Connection", "Connection", "Keep-Alive", "Trailer", "Upgrade"} {
		headers.Del(name)
	}
	proxyURL, err := nativeProxyURL(auth)
	if err != nil {
		return nativeError("proxy_error", "invalid OpenCode proxy configuration", 400)
	}
	id := headerGet(req.Headers, nativeExecutionIDHeader)
	if id == "" {
		id = newRequestID()
	}
	ctx, cleanup := m.nativeContext(id)
	client, err := m.nativeClient(proxyURL)
	if err != nil {
		cleanup()
		return nativeError("proxy_error", "invalid OpenCode proxy configuration", 400)
	}
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, auth.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		cleanup()
		client.CloseIdleConnections()
		return nativeError("invalid_request", "invalid OpenCode request", 400)
	}
	upstream.Header = headers
	response, err := client.Do(upstream)
	if err != nil {
		cleanup()
		client.CloseIdleConnections()
		return nativeError("upstream_error", "OpenCode request failed: "+redactNativeError(err.Error(), auth.APIKey), 502)
	}
	finish := func() { _ = response.Body.Close(); client.CloseIdleConnections(); cleanup() }
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer finish()
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
		detail := strings.TrimSpace(string(data))
		if detail == "" {
			detail = http.StatusText(response.StatusCode)
		}
		return nativeUpstreamError(response.StatusCode, redactNativeError(detail, auth.APIKey), response.Header.Get("Retry-After"))
	}
	if !strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		finish()
		return nativeError("upstream_error", "OpenCode returned a non-streaming response", 502)
	}
	responseHeaders := nativeResponseHeaders(response.Header)
	if stream {
		source := strings.ToLower(headerGet(req.Headers, nativeSourceFormatHeader))
		if source == "" {
			source = strings.ToLower(req.SourceFormat)
		}
		go func() {
			defer finish()
			err := readNativeSSE(response.Body, func(frame, data []byte) error {
				if string(bytes.TrimSpace(data)) == "[DONE]" {
					return nil
				}
				// Chat adds SSE framing; translated protocols consume upstream SSE events.
				chunk := frame
				switch source {
				case "", "openai", "chat-completions", "openai-chat-completions", "openai_chat_completions":
					chunk = data
				}
				return nativeHostCall("host.stream.emit", map[string]any{"stream_id": req.StreamID, "payload": chunk}, nil)
			})
			closeReq := map[string]any{"stream_id": req.StreamID}
			if err != nil {
				closeReq["error_detail"] = &EnvelopeError{Code: "upstream_error", Message: redactNativeError(err.Error(), auth.APIKey), HTTPStatus: 502}
			}
			_ = nativeHostCall("host.stream.close", closeReq, nil)
		}()
		return OKEnvelope(map[string]any{"headers": responseHeaders})
	}
	defer finish()
	completion := nativeCompletion{root: map[string]any{}, choices: map[int]map[string]any{}}
	if err := readNativeSSE(response.Body, func(frame, data []byte) error { return completion.add(data) }); err != nil {
		return nativeError("upstream_error", redactNativeError(err.Error(), auth.APIKey), 502)
	}
	data, err := completion.result()
	if err != nil {
		return nativeError("upstream_error", err.Error(), 502)
	}
	responseHeaders.Set("Content-Type", "application/json")
	return OKEnvelope(struct {
		Payload []byte
		Headers http.Header
	}{data, responseHeaders})
}

func redactNativeError(message, key string) string {
	if key != "" {
		message = strings.ReplaceAll(message, key, "[redacted]")
	}
	return message
}

func nativeUpstreamError(status int, message, retry string) []byte {
	seconds, _ := strconv.Atoi(strings.TrimSpace(retry))
	if seconds < 0 {
		seconds = 0
	}
	if seconds == 0 {
		if at, err := http.ParseTime(retry); err == nil {
			seconds = int(time.Until(at).Seconds())
			if seconds < 0 {
				seconds = 0
			}
		}
	}
	raw, _ := json.Marshal(Envelope{Error: &EnvelopeError{Code: "upstream_error", Message: message, HTTPStatus: status, RetryAfterSeconds: seconds}})
	return raw
}

func nativeResponseHeaders(headers http.Header) http.Header {
	out := headers.Clone()
	for _, name := range []string{"Content-Length", "Content-Encoding", "Transfer-Encoding", "Connection", "Keep-Alive", "Trailer", "Upgrade"} {
		out.Del(name)
	}
	return out
}

// Emit complete SSE events so host protocol translators never receive split JSON.
func readNativeSSE(body io.Reader, emit func(frame, data []byte) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var frame bytes.Buffer
	var data []string
	done := false
	flush := func() error {
		if frame.Len() == 0 {
			return nil
		}
		payload := []byte(strings.Join(data, "\n"))
		raw := append([]byte(nil), frame.Bytes()...)
		frame.Reset()
		data = nil
		if len(payload) == 0 {
			return nil
		}
		if strings.TrimSpace(string(payload)) == "[DONE]" {
			done = true
		} else {
			var event map[string]json.RawMessage
			if json.Unmarshal(payload, &event) != nil {
				return errors.New("OpenCode returned an invalid SSE JSON event")
			}
			if errorPayload, ok := event["error"]; ok {
				return fmt.Errorf("OpenCode stream error: %s", errorPayload)
			}
		}
		return emit(raw, payload)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if frame.Len()+len(line) > 8*1024*1024 {
			return errors.New("OpenCode SSE event exceeds 8 MiB")
		}
		frame.WriteString(line)
		frame.WriteByte('\n')
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			if done {
				return nil
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if frame.Len() > 0 {
		frame.WriteByte('\n')
		if err := flush(); err != nil {
			return err
		}
	}
	if !done {
		return errors.New("OpenCode stream ended before [DONE]")
	}
	return nil
}

type nativeCompletion struct {
	root    map[string]any
	choices map[int]map[string]any
	size    int
}

func (c *nativeCompletion) add(data []byte) error {
	if string(bytes.TrimSpace(data)) == "[DONE]" {
		return nil
	}
	c.size += len(data)
	if c.size > 64*1024*1024 {
		return errors.New("OpenCode completion exceeds 64 MiB")
	}
	var chunk map[string]any
	if err := json.Unmarshal(data, &chunk); err != nil {
		return err
	}
	for key, value := range chunk {
		if key != "choices" && key != "object" {
			c.root[key] = value
		}
	}
	choices, _ := chunk["choices"].([]any)
	for _, item := range choices {
		choice, ok := item.(map[string]any)
		if !ok {
			continue
		}
		index := nativeIndex(choice["index"])
		out := c.choices[index]
		if out == nil {
			out = map[string]any{"index": index, "message": map[string]any{"role": "assistant", "content": ""}, "finish_reason": nil}
			c.choices[index] = out
		}
		for key, value := range choice {
			if key != "delta" && key != "message" && value != nil {
				out[key] = value
			}
		}
		if delta, ok := choice["delta"].(map[string]any); ok {
			mergeNativeDelta(out["message"].(map[string]any), delta)
		}
	}
	return nil
}

func nativeIndex(value any) int { n, _ := value.(float64); return int(n) }

func mergeNativeDelta(dst, delta map[string]any) {
	for key, value := range delta {
		switch key {
		case "content", "reasoning_content", "reasoning", "refusal":
			if part, ok := value.(string); ok {
				prior, _ := dst[key].(string)
				dst[key] = prior + part
			} else if value != nil {
				dst[key] = value
			}
		case "function_call":
			if function, ok := value.(map[string]any); ok {
				target, _ := dst[key].(map[string]any)
				if target == nil {
					target = map[string]any{}
					dst[key] = target
				}
				mergeNativeFunction(target, function)
			}
		case "tool_calls":
			parts, _ := value.([]any)
			tools, _ := dst[key].([]any)
			for _, part := range parts {
				tool, ok := part.(map[string]any)
				if !ok {
					continue
				}
				index := nativeIndex(tool["index"])
				if index < 0 || index > 1024 {
					continue
				}
				for len(tools) <= index {
					tools = append(tools, map[string]any{})
				}
				target := tools[index].(map[string]any)
				for name, value := range tool {
					if name == "index" {
						continue
					}
					if name == "function" {
						function, _ := value.(map[string]any)
						current, _ := target[name].(map[string]any)
						if current == nil {
							current = map[string]any{}
							target[name] = current
						}
						mergeNativeFunction(current, function)
					} else {
						target[name] = value
					}
				}
			}
			dst[key] = tools
		default:
			if value != nil {
				dst[key] = value
			}
		}
	}
}

func mergeNativeFunction(dst, part map[string]any) {
	for key, value := range part {
		if key == "arguments" {
			prior, _ := dst[key].(string)
			text, _ := value.(string)
			dst[key] = prior + text
		} else {
			dst[key] = value
		}
	}
}

func (c *nativeCompletion) result() ([]byte, error) {
	if len(c.choices) == 0 {
		return nil, errors.New("OpenCode completion contains no choices")
	}
	indices := make([]int, 0, len(c.choices))
	for index := range c.choices {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	choices := make([]any, 0, len(indices))
	for _, index := range indices {
		choices = append(choices, c.choices[index])
	}
	c.root["choices"] = choices
	c.root["object"] = "chat.completion"
	return json.Marshal(c.root)
}
