package plugin

import (
	"bufio"
	"context"
	stdtls "crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	tls "github.com/refraction-networking/utls"
	"golang.org/x/net/proxy"
)

func newNativeHTTPClient(proxyURL string) (*http.Client, error) {
	transport, err := newNativeTransport(proxyURL)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func newNativeTransport(proxyURL string) (*http.Transport, error) {
	proxyURL = normalizeNativeProxy(proxyURL)
	direct := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	var forward proxy.ContextDialer = direct
	var proxyEndpoint *url.URL
	if proxyURL != "" && proxyURL != "direct://" {
		var err error
		proxyEndpoint, err = url.Parse(proxyURL)
		if err != nil || proxyEndpoint.Hostname() == "" {
			return nil, errors.New("invalid OpenCode proxy URL")
		}
		switch proxyEndpoint.Scheme {
		case "socks5", "socks5h":
			p, err := proxy.FromURL(proxyEndpoint, direct)
			if err != nil {
				return nil, errors.New("invalid OpenCode SOCKS proxy")
			}
			var ok bool
			forward, ok = p.(proxy.ContextDialer)
			if !ok {
				return nil, errors.New("OpenCode proxy must support cancellation")
			}
		case "http", "https":
		default:
			return nil, errors.New("unsupported OpenCode proxy scheme")
		}
	}
	transport := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 45 * time.Second, IdleConnTimeout: 30 * time.Second}
	transport.DialContext = forward.DialContext
	transport.TLSNextProto = map[string]func(string, *stdtls.Conn) http.RoundTripper{}
	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		var conn net.Conn
		var err error
		if proxyEndpoint != nil && (proxyEndpoint.Scheme == "http" || proxyEndpoint.Scheme == "https") {
			conn, err = nativeProxyConnect(ctx, proxyEndpoint, addr)
		} else {
			conn, err = transport.DialContext(ctx, network, addr)
		}
		if err != nil {
			return nil, err
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		config := &tls.Config{ServerName: host}
		if transport.TLSClientConfig != nil {
			config.RootCAs = transport.TLSClientConfig.RootCAs
		}
		secured := tls.UClient(conn, config, tls.HelloCustom)
		if err = secured.ApplyPreset(nativeClientHelloSpec()); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("OpenCode TLS profile: %w", err)
		}
		handshakeCtx, cancel := context.WithTimeout(ctx, transport.TLSHandshakeTimeout)
		defer cancel()
		if err = secured.HandshakeContext(handshakeCtx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("OpenCode TLS handshake: %w", err)
		}
		return secured, nil
	}
	return transport, nil
}

func nativeProxyConnect(ctx context.Context, endpoint *url.URL, target string) (net.Conn, error) {
	port := endpoint.Port()
	if port == "" {
		port = "80"
		if endpoint.Scheme == "https" {
			port = "443"
		}
	}
	conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(endpoint.Hostname(), port))
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if endpoint.Scheme == "https" {
		secured := stdtls.Client(conn, &stdtls.Config{ServerName: endpoint.Hostname(), MinVersion: stdtls.VersionTLS12})
		if err = secured.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = secured
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: http.Header{}}
	if endpoint.User != nil {
		password, _ := endpoint.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(endpoint.User.Username()+":"+password)))
	}
	if err = req.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenCode proxy CONNECT returned HTTP %d", response.StatusCode)
	}
	if reader.Buffered() != 0 {
		return nil, errors.New("unexpected bytes after proxy CONNECT")
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	return conn, nil
}

// Each handshake gets fresh key shares and the verified Bun 1.4.4 ClientHello.
func nativeClientHelloSpec() *tls.ClientHelloSpec {
	return &tls.ClientHelloSpec{CipherSuites: []uint16{0x1301, 0x1302, 0x1303, 0xc02b, 0xc02f, 0xc02c, 0xc030, 0xcca9, 0xcca8, 0xc009, 0xc013, 0xc00a, 0xc014, 0x009c, 0x009d, 0x002f, 0x0035}, CompressionMethods: []uint8{0}, Extensions: []tls.TLSExtension{
		&tls.SNIExtension{}, &tls.ExtendedMasterSecretExtension{}, &tls.RenegotiationInfoExtension{Renegotiation: tls.RenegotiateOnceAsClient},
		&tls.SupportedCurvesExtension{Curves: []tls.CurveID{tls.X25519MLKEM768, tls.X25519, tls.CurveP256, tls.CurveP384}}, &tls.SupportedPointsExtension{SupportedPoints: []uint8{0}}, &tls.SessionTicketExtension{}, &tls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}}, &tls.StatusRequestExtension{},
		&tls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256, tls.PSSWithSHA256, tls.PKCS1WithSHA256, tls.ECDSAWithP384AndSHA384, tls.PSSWithSHA384, tls.PKCS1WithSHA384, tls.PSSWithSHA512, tls.PKCS1WithSHA512, tls.Ed25519, tls.ECDSAWithP521AndSHA512, tls.PKCS1WithSHA1}},
		&tls.SCTExtension{}, &tls.KeyShareExtension{KeyShares: []tls.KeyShare{{Group: tls.X25519MLKEM768}, {Group: tls.X25519}}}, &tls.PSKKeyExchangeModesExtension{Modes: []uint8{tls.PskModeDHE}}, &tls.SupportedVersionsExtension{Versions: []uint16{tls.VersionTLS13, tls.VersionTLS12}},
	}}
}

func nativeProxyURL(auth nativeAuth) (string, error) {
	if selected := normalizeNativeProxy(auth.ProxyURL); selected != "" {
		return selected, nil
	}
	u, _ := url.Parse(auth.BaseURL)
	selected, err := http.ProxyFromEnvironment(&http.Request{URL: u})
	if err != nil {
		return "", err
	}
	if selected != nil {
		return selected.String(), nil
	}
	return "", nil
}

func normalizeNativeProxy(value string) string {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "direct") || strings.EqualFold(value, "none") || strings.EqualFold(value, "direct://") {
		return "direct://"
	}
	return value
}
