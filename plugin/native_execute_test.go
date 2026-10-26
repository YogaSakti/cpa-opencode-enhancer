package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type nativeRoundTripper func(*http.Request) (*http.Response, error)

func (f nativeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func nativeTestManager(t *testing.T, handler http.HandlerFunc) *Manager {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	m := NewManager()
	m.nativeClient = func(string) (*http.Client, error) {
		return &http.Client{Transport: nativeRoundTripper(func(req *http.Request) (*http.Response, error) {
			clone := req.Clone(req.Context())
			clone.URL, _ = url.Parse(server.URL + req.URL.Path)
			return server.Client().Transport.RoundTrip(clone)
		})}, nil
	}
	return m
}
func nativeTestRequest() nativeExecutorRequest {
	return nativeExecutorRequest{AuthProvider: PluginID, AuthID: PluginID + ":zen.json", Model: "mimo", SourceFormat: "openai", Format: "openai", Payload: []byte(`{"model":"mimo","messages":[{"role":"user","content":"Reply OK"}]}`), StorageJSON: []byte(`{"type":"opencode-enhancer","api_key":"upstream-key","models":[{"name":"mimo-free","alias":"mimo"}]}`), Headers: http.Header{"Authorization": []string{"Bearer downstream-key"}, nativeExecutionIDHeader: []string{"execution-1"}}, StreamID: "stream-1"}
}

func TestNativeExecuteAggregatesCompletionAndPreservesTools(t *testing.T) {
	m := nativeTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-key" || r.Header.Get(nativeExecutionIDHeader) != "" {
			t.Errorf("wrong upstream credential or internal header leaked")
		}
		if !isOpenCodeSessionID(r.Header.Get("X-Opencode-Session")) {
			t.Error("missing shaped OpenCode session")
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if req["model"] != "mimo-free" || req["stream"] != true || !strings.Contains(string(body), "Working directory:") {
			t.Errorf("unshaped body %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"chat-1\",\"model\":\"mimo-free\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"O\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"K\",\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"total_tokens\":3}}\n\ndata: [DONE]\n\n")
	})
	response := nativeCall(t, m, "executor.execute", nativeTestRequest())
	if !response.OK {
		t.Fatalf("execute: %+v", response.Error)
	}
	var result struct {
		Payload []byte
		Headers http.Header
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	var completion struct {
		Object  string
		Choices []struct {
			Message struct {
				Content   string
				ToolCalls []struct{ Function struct{ Arguments string } } `json:"tool_calls"`
			}
			FinishReason string `json:"finish_reason"`
		}
	}
	if err := json.Unmarshal(result.Payload, &completion); err != nil {
		t.Fatal(err)
	}
	if completion.Object != "chat.completion" || len(completion.Choices) != 1 || completion.Choices[0].Message.Content != "OK" || completion.Choices[0].Message.ToolCalls[0].Function.Arguments != "{}" || completion.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("completion: %s", result.Payload)
	}
}

func TestNativeStreamEmitsBeforeCompletionAndCancels(t *testing.T) {
	disconnected := make(chan struct{})
	m := nativeTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(disconnected)
	})
	emitted := make(chan string, 4)
	SetHostCaller(func(method string, payload []byte) ([]byte, error) {
		if method == "host.stream.emit" {
			var req struct {
				Payload []byte `json:"payload"`
			}
			_ = json.Unmarshal(payload, &req)
			emitted <- string(req.Payload)
		}
		return OKEnvelope(struct{}{}), nil
	})
	defer SetHostCaller(nil)
	response := nativeCall(t, m, "executor.execute_stream", nativeTestRequest())
	if !response.OK {
		t.Fatalf("stream: %+v", response.Error)
	}
	select {
	case chunk := <-emitted:
		if !strings.Contains(chunk, "OK") {
			t.Fatalf("unexpected chunk %s", chunk)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first chunk was buffered")
	}
	completed := nativeCall(t, m, "request.complete", map[string]string{"RequestID": "execution-1"})
	if !completed.OK {
		t.Fatal("complete failed")
	}
	select {
	case <-disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not reach upstream")
	}
}

func TestNativeErrorsKeepHTTPStatusAndRequireDone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		retry  string
		want   int
	}{{"quota", 429, `{"error":{"message":"limited"}}`, "17", 429}, {"truncated", 200, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n", "", 502}} {
		t.Run(tc.name, func(t *testing.T) {
			m := nativeTestManager(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Retry-After", tc.retry)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			response := nativeCall(t, m, "executor.execute", nativeTestRequest())
			if response.OK || response.Error.HTTPStatus != tc.want {
				t.Fatalf("error = %+v", response.Error)
			}
			if tc.retry != "" && response.Error.RetryAfterSeconds != 17 {
				t.Fatalf("retry lost: %+v", response.Error)
			}
		})
	}
}

func TestNativeRejectsUnlistedModelBeforeNetwork(t *testing.T) {
	m := NewManager()
	m.nativeClient = func(string) (*http.Client, error) {
		t.Fatal("unlisted model made network call")
		return nil, context.Canceled
	}
	req := nativeTestRequest()
	req.Model = "unknown-free"
	response := nativeCall(t, m, "executor.execute", req)
	if response.OK || response.Error.HTTPStatus != 400 {
		t.Fatalf("response = %+v", response.Error)
	}
}

func TestNativeStreamPayloadMatchesHostClientFormat(t *testing.T) {
	for _, source := range []string{"openai", "claude", "openai-response", "gemini"} {
		t.Run(source, func(t *testing.T) {
			m := nativeTestManager(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-CPA-OpenCode-Source-Format") != "" {
					t.Error("internal source format leaked upstream")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n\n")
			})
			chunks := make(chan []byte, 4)
			closed := make(chan struct{})
			SetHostCaller(func(method string, payload []byte) ([]byte, error) {
				if method == "host.stream.emit" {
					var req struct {
						Payload []byte `json:"payload"`
					}
					_ = json.Unmarshal(payload, &req)
					chunks <- req.Payload
				}
				if method == "host.stream.close" {
					close(closed)
				}
				return OKEnvelope(struct{}{}), nil
			})
			defer SetHostCaller(nil)
			req := nativeTestRequest()
			req.SourceFormat = "openai"
			req.Headers.Set("X-CPA-OpenCode-Source-Format", source)
			response := nativeCall(t, m, "executor.execute_stream", req)
			if !response.OK {
				t.Fatalf("stream: %+v", response.Error)
			}
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("stream did not close")
			}
			if len(chunks) != 1 {
				t.Fatalf("emitted %d chunks; host supplies terminal DONE", len(chunks))
			}
			chunk := <-chunks
			if source == "openai" {
				if !json.Valid(chunk) {
					t.Fatalf("Chat host requires bare JSON; got %q", chunk)
				}
			} else if !strings.HasPrefix(string(chunk), "data: ") {
				t.Fatalf("translated host requires framed data; got %q", chunk)
			}
		})
	}
}

func TestNativeWhitespaceAPIKeyIsRedactedFromHTTPAndStreamErrors(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			m := nativeTestManager(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer upstream-key" {
					t.Error("transmitted API key was not normalized")
				}
				if !stream {
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, `{"error":{"message":"echo upstream-key"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"error\":{\"message\":\"echo upstream-key\"}}\n\n")
			})
			req := nativeTestRequest()
			req.StorageJSON = []byte(`{"type":"opencode-enhancer","api_key":" upstream-key ","models":[{"name":"mimo-free","alias":"mimo"}]}`)
			message := ""
			if !stream {
				response := nativeCall(t, m, "executor.execute", req)
				if response.OK || response.Error == nil {
					t.Fatal("expected HTTP error")
				}
				message = response.Error.Message
			} else {
				closed := make(chan string, 1)
				SetHostCaller(func(method string, payload []byte) ([]byte, error) {
					if method == "host.stream.close" {
						var closeReq struct {
							Detail *EnvelopeError `json:"error_detail"`
						}
						_ = json.Unmarshal(payload, &closeReq)
						if closeReq.Detail == nil {
							closed <- ""
						} else {
							closed <- closeReq.Detail.Message
						}
					}
					return OKEnvelope(struct{}{}), nil
				})
				defer SetHostCaller(nil)
				response := nativeCall(t, m, "executor.execute_stream", req)
				if !response.OK {
					t.Fatalf("expected stream headers: %+v", response.Error)
				}
				select {
				case message = <-closed:
				case <-time.After(3 * time.Second):
					t.Fatal("stream error did not close")
				}
			}
			if message == "" || strings.Contains(message, "upstream-key") || !strings.Contains(message, "[redacted]") {
				t.Errorf("sent API key was not redacted from error")
			}
		})
	}
}
