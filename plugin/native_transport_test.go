package plugin

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestNativeTransportUsesVerifiedProfileAndValidatesCertificates(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"opencode.ai"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	protos := make(chan []string, 2)
	curves := make(chan []tls.CurveID, 2)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Proto != "HTTP/1.1" {
			t.Errorf("protocol=%s", r.Proto)
		}
		w.WriteHeader(200)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, GetConfigForClient: func(info *tls.ClientHelloInfo) (*tls.Config, error) {
		protos <- info.SupportedProtos
		curves <- info.SupportedCurves
		return nil, nil
	}, NextProtos: []string{"h2", "http/1.1"}}
	server.StartTLS()
	defer server.Close()
	for _, trusted := range []bool{true, false} {
		transport, err := newNativeTransport("direct://")
		if err != nil {
			t.Fatal(err)
		}
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		}
		if trusted {
			roots := x509.NewCertPool()
			roots.AddCert(cert)
			transport.TLSClientConfig = &tls.Config{RootCAs: roots}
		}
		client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
		response, err := client.Get("https://opencode.ai/profile-test")
		if trusted {
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
		} else if err == nil {
			_ = response.Body.Close()
			t.Fatal("untrusted certificate was accepted")
		}
		transport.CloseIdleConnections()
		if got := <-protos; !reflect.DeepEqual(got, []string{"http/1.1"}) {
			t.Fatalf("ALPN=%v", got)
		}
		if got := <-curves; !reflect.DeepEqual(got, []tls.CurveID{tls.X25519MLKEM768, tls.X25519, tls.CurveP256, tls.CurveP384}) {
			t.Fatalf("groups=%v", got)
		}
	}
}

func TestNativeContextOldAttemptCleanupCannotForgetReplacement(t *testing.T) {
	m := NewManager()
	first, finishFirst := m.nativeContext("execution")
	second, finishSecond := m.nativeContext("execution")
	defer finishSecond()
	if first.Err() != context.Canceled {
		t.Fatal("old attempt remained active")
	}
	finishFirst()
	nativeCall(t, m, "request.complete", map[string]string{"RequestID": "execution"})
	if second.Err() != context.Canceled {
		t.Fatal("old cleanup removed new attempt cancellation")
	}
}

func TestNativeInterceptorCarriesLifecycleIDOnlyForNativeCredentials(t *testing.T) {
	m := NewManager()
	response := nativeCall(t, m, "request.intercept_after", RequestInterceptRequest{RequestID: "execution-1", SourceFormat: "claude", Model: "mimo", Headers: http.Header{}, Metadata: map[string]any{"selected_auth_id": PluginID + ":zen.json"}})
	var result RequestInterceptResponse
	_ = json.Unmarshal(response.Result, &result)
	if result.Headers.Get(nativeExecutionIDHeader) != "execution-1" {
		t.Fatal("native lifecycle ID missing")
	}
	if result.Headers.Get("X-CPA-OpenCode-Source-Format") != "claude" {
		t.Fatal("original source format missing before host translation")
	}
}

func TestNativeProxySentinelsAndWhitespace(t *testing.T) {
	for _, value := range []string{"none", " DIRECT ", "direct://"} {
		transport, err := newNativeTransport(value)
		if err != nil {
			t.Fatalf("direct sentinel %q: %v", value, err)
		}
		transport.CloseIdleConnections()
		selected, err := nativeProxyURL(nativeAuth{BaseURL: nativeBaseURL, ProxyURL: value})
		if err != nil || selected != "direct://" {
			t.Fatalf("proxy %q = %q, %v", value, selected, err)
		}
	}
	storage := []byte(`{"type":"opencode-enhancer","api_key":"secret","proxy_url":"  ","models":[]}`)
	response := nativeCall(t, NewManager(), "auth.parse", map[string]any{"Provider": PluginID, "FileName": "zen.json", "RawJSON": storage, "Host": map[string]string{"ProxyURL": " http://localhost:3128 "}})
	var result struct{ Auth nativeAuthData }
	_ = json.Unmarshal(response.Result, &result)
	if !response.OK || result.Auth.ProxyURL != "http://localhost:3128" || result.Auth.Attributes["proxy_url"] != "http://localhost:3128" {
		t.Fatal("whitespace credential proxy bypassed global fallback")
	}
}

func TestNativeTransportRestrictsOnlyDirectOpenCodeToIPv4(t *testing.T) {
	for _, tc := range []struct{ proxy, host, network string }{
		{"", "opencode.ai:443", "tcp4"},
		{"direct://", "opencode.ai:443", "tcp4"},
		{"none", "OPENCODE.AI:443", "tcp4"},
		{"socks5://localhost:1080", "opencode.ai:443", "tcp"},
		{"socks5h://localhost:1080", "opencode.ai:443", "tcp"},
		{"", "example.org:443", "tcp"},
	} {
		t.Run(tc.proxy+tc.host, func(t *testing.T) {
			transport, err := newNativeTransport(tc.proxy)
			if err != nil {
				t.Fatal(err)
			}
			defer transport.CloseIdleConnections()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			called := false
			transport.DialContext = func(got context.Context, network, addr string) (net.Conn, error) {
				called = true
				if got != ctx || got.Err() != context.Canceled {
					t.Error("dial cancellation context changed")
				}
				if network != tc.network || addr != tc.host {
					t.Errorf("dial(%q,%q), want(%q,%q)", network, addr, tc.network, tc.host)
				}
				return nil, got.Err()
			}
			_, err = transport.DialTLSContext(ctx, "tcp", tc.host)
			if !called || !errors.Is(err, context.Canceled) {
				t.Fatalf("dial cancellation lost: %v", err)
			}
		})
	}
}
