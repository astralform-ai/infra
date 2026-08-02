//go:build linux

package tcpfirewall

// Transparent HTTPS header injection ("transform.headers") for egress hosts that carry a
// per-domain transform rule. e2b's OSS data path only does SNI allow/deny + TCP tunnel;
// this adds the missing injection so `network.rules[host].transform.headers` (already in
// the proto + stored per sandbox) is actually applied. Injection is scoped to hosts that
// have a transform — every other host keeps the untouched SNI-tunnel fast path.
//
// Mechanism: for a matched transform host we MITM the sandbox's TLS with a leaf cert minted
// by an egress CA the sandbox trusts (served via CABundle()), read each HTTP/1.1 request,
// set the configured headers, and forward over a fresh TLS connection to the real origin
// (with the same internal-IP guard as the tunnel path).
//
// WHAT THIS DOES AND DOES NOT PROTECT — read before relying on it.
//
// It keeps the credential out of the sandbox's own state: not in the environment, not on
// disk, not in the process table. Code in the sandbox holds only a placeholder and cannot
// read the real value out of itself.
//
// It does NOT stop an ORIGIN from handing the value back. We strip the injected header
// names from the response headers below, but any origin that reflects request headers into
// its response BODY — postman-echo, httpbin/headers, most debug endpoints, or a hostile
// server that base64s it — returns the credential to the sandbox, and no proxy can prevent
// that in general. Verified live on 2026-08-02: a probe against postman-echo.com read its
// own injected header straight out of the JSON body.
//
// So injection converts "the secret sits in os.environ forever" into "the secret is
// reachable only by talking to an allowlisted host that chooses to echo it". That is a real
// reduction and it is NOT containment. Scope allowed hosts to origins you trust not to
// reflect, and keep the egress allowlist tight — that, not this file, bounds the blast
// radius. Cloudflare's and Vercel's equivalents share this property.

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/e2b-dev/infra/packages/orchestrator/pkg/sandbox"
	"github.com/e2b-dev/infra/packages/shared/pkg/logger"
)

// mitmCA is a self-signed egress CA that mints (and caches) per-host leaf certificates for
// TLS interception. Generated once per orchestrator process; its cert is handed to every
// sandbox via CABundle() so minted leaves validate. A restart rotates the CA — only
// sandboxes created before the restart are affected (persistence is a follow-up).
type mitmCA struct {
	cert   *x509.Certificate
	key    *ecdsa.PrivateKey
	pem    string
	leaves sync.Map // host -> *tls.Certificate
}

func newMITMCA() (*mitmCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("egress CA key: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "e2b egress transform CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("egress CA cert: %w", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse egress CA cert: %w", err)
	}

	return &mitmCA{
		cert: cert,
		key:  key,
		pem:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}, nil
}

// certForHost mints (once, then caches) a leaf certificate for host signed by the CA.
func (c *mitmCA) certForHost(host string) (*tls.Certificate, error) {
	if v, ok := c.leaves.Load(host); ok {
		return v.(*tls.Certificate), nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}

	leaf := &tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: key}
	actual, _ := c.leaves.LoadOrStore(host, leaf)

	return actual.(*tls.Certificate), nil
}

// transformHeadersForHost returns the merged transform headers configured for a hostname,
// or nil if the sandbox has no matching transform rule. Rule keys reuse the same domain
// matching as the allowlist (exact, "*", or "*.suffix").
func transformHeadersForHost(sbx *sandbox.Sandbox, hostname string) map[string]string {
	if hostname == noHostnameValue {
		return nil
	}

	egress := sbx.Config.GetNetworkEgress()
	if egress == nil {
		return nil
	}

	var headers map[string]string
	for pattern, domainRules := range egress.GetRules() {
		if !matchDomain(hostname, pattern) {
			continue
		}
		for _, rule := range domainRules.GetRules() {
			for k, v := range rule.GetTransform().GetHeaders() {
				if headers == nil {
					headers = make(map[string]string)
				}
				headers[k] = v
			}
		}
	}

	return headers
}

// mitmInject terminates the sandbox's TLS with a minted leaf, injects the transform headers
// into each HTTP/1.1 request, and forwards over a fresh TLS connection to the real origin.
func (p *Proxy) mitmInject(ctx context.Context, conn net.Conn, host string, port int, headers map[string]string, logger logger.Logger, metrics *Metrics) {
	tracker := metrics.TrackConnection(ProtocolTLS)
	defer tracker.Close(ctx)
	defer conn.Close()

	serverConn := tls.Server(conn, &tls.Config{
		// Force HTTP/1.1 so we never have to speak h2 to the sandbox after interception.
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := chi.ServerName
			if name == "" {
				name = host
			}

			return p.mitmCA.certForHost(name)
		},
	})

	handshakeCtx, cancel := context.WithTimeout(ctx, upstreamDialTimeout)
	defer cancel()
	if err := serverConn.HandshakeContext(handshakeCtx); err != nil {
		logger.Warn(ctx, "egress transform: client TLS handshake failed", zap.String("host", host), zap.Error(err))
		metrics.RecordError(ctx, ErrorTypeUpstreamDial, ProtocolTLS)

		return
	}

	transport := p.injectTransport(ctx, host, logger, metrics)
	defer transport.CloseIdleConnections()

	upstreamHostPort := net.JoinHostPort(host, strconv.Itoa(port))
	br := bufio.NewReader(serverConn)

	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return // EOF / client closed / malformed — done with this connection
		}

		for k, v := range headers {
			req.Header.Set(k, v)
		}

		req.URL.Scheme = "https"
		req.URL.Host = upstreamHostPort
		req.RequestURI = "" // required: RoundTrip rejects a set RequestURI
		req.Host = host

		resp, err := transport.RoundTrip(req.WithContext(ctx))
		if err != nil {
			// Never include the error's request context here — a transport error can carry
			// the URL and headers, and these headers hold the injected credential.
			logger.Warn(ctx, "egress transform: upstream request failed", zap.String("host", host), zap.Error(err))
			metrics.RecordError(ctx, ErrorTypeUpstreamDial, ProtocolTLS)
			_ = writeGatewayError(serverConn)

			return
		}

		// Strip the injected header names from the RESPONSE headers before the sandbox sees
		// them. Some origins echo request headers straight back (CORS preflight mirrors,
		// debug endpoints), which would hand the credential to sandbox code for free.
		//
		// This closes the header path only. An origin that reflects into the response BODY
		// still returns the value and cannot be stopped from here — see the note at the top
		// of this file. Do not read this as containment.
		for k := range headers {
			resp.Header.Del(k)
		}

		writeErr := resp.Write(serverConn)
		_ = resp.Body.Close()
		if writeErr != nil {
			return
		}

		if req.Close || resp.Close {
			return
		}
	}
}

// injectTransport dials the real origin over TLS, applying the same internal-IP guard as the
// tunnel path (block hostnames that resolve to private/internal ranges).
func (p *Proxy) injectTransport(ctx context.Context, host string, logger logger.Logger, metrics *Metrics) *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2: false,
		DialTLSContext: func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{
				Timeout: upstreamDialTimeout,
				ControlContext: func(_ context.Context, _, address string, _ syscall.RawConn) error {
					h, _, err := net.SplitHostPort(address)
					if err != nil {
						return fmt.Errorf("parse resolved address %q: %w", address, err)
					}
					ip := net.ParseIP(h)
					if ip == nil {
						return fmt.Errorf("parse IP from %q", h)
					}
					if isIPInAlwaysDeniedCIDRs(ip) {
						logger.Warn(ctx, "egress transform: blocked internal IP", zap.String("host", host), zap.String("ip", ip.String()))
						metrics.RecordError(ctx, ErrorTypeResolvedIPBlocked, ProtocolTLS)

						return fmt.Errorf("host resolved to internal IP %s", ip)
					}

					return nil
				},
			}

			raw, err := dialer.DialContext(dialCtx, network, addr)
			if err != nil {
				return nil, err
			}

			tlsConn := tls.Client(raw, &tls.Config{ServerName: host})
			if err := tlsConn.HandshakeContext(dialCtx); err != nil {
				_ = raw.Close()

				return nil, err
			}

			return tlsConn, nil
		},
	}
}

func writeGatewayError(w net.Conn) error {
	_, err := w.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))

	return err
}
