//go:build linux

package tcpfirewall

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"

	"github.com/e2b-dev/infra/packages/shared/pkg/logger"
)

// TestMITMInjectsTransformHeader drives the real injection path end-to-end: a client TLS
// connection (trusting the egress CA) → mitmInject (terminates TLS with a minted leaf,
// injects the header) → a live upstream that reflects request headers. Proves the origin
// receives the injected header while the "sandbox" side only ever sent a plain request.
// Requires outbound internet (uses postman-echo.com/get, which echoes request headers).
func TestMITMInjectsTransformHeader(t *testing.T) {
	ca, err := newMITMCA()
	if err != nil {
		t.Fatal(err)
	}

	p := &Proxy{mitmCA: ca}
	metrics := NewMetrics(noop.NewMeterProvider())
	lg := logger.NewNopLogger()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	const host = "postman-echo.com"
	headers := map[string]string{"X-Injected-Secret": "sekret-42"}

	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		p.mitmInject(context.Background(), conn, host, 443, headers, lg, metrics)
	}()

	raw, err := net.DialTimeout("tcp", ln.Addr().String(), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(ca.pem)) {
		t.Fatal("failed to load egress CA into pool")
	}

	client := tls.Client(raw, &tls.Config{RootCAs: pool, ServerName: host})
	defer client.Close()

	req, _ := http.NewRequest(http.MethodGet, "https://"+host+"/get", nil)
	req.Header.Set("Connection", "close")
	if err := req.Write(client); err != nil {
		t.Fatalf("write request through MITM: %v", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(client), req)
	if err != nil {
		t.Fatalf("read response through MITM: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upstream status %d: %s", resp.StatusCode, body)
	}

	// postman-echo /get reflects the request headers back in its JSON body.
	if !strings.Contains(strings.ToLower(string(body)), "sekret-42") {
		t.Fatalf("injected header not seen by origin; body: %s", body)
	}
}

// TestTransformHeadersMatching covers the rule→header lookup (exact + wildcard), independent
// of any network.
func TestMatchDomainForTransform(t *testing.T) {
	cases := []struct {
		host, pattern string
		want          bool
	}{
		{"api.openai.com", "api.openai.com", true},
		{"api.openai.com", "*.openai.com", true},
		{"api.openai.com", "*", true},
		{"api.openai.com", "openai.com", false},
		{"evil.com", "api.openai.com", false},
	}
	for _, c := range cases {
		if got := matchDomain(c.host, c.pattern); got != c.want {
			t.Errorf("matchDomain(%q,%q)=%v want %v", c.host, c.pattern, got, c.want)
		}
	}
}
