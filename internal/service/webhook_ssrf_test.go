package service

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The private-address check lives in the dialer, not in URL parsing,
// so it fires on the resolved IP — a hostname that maps to 127.0.0.1
// is caught the same as a literal, and so is a redirect to one.
func TestWebhookClientBlocksPrivateTargets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	}))
	defer srv.Close()

	blocked, _, _ := newWebhookClient(2*time.Second, false)
	_, err := blocked.Post(srv.URL, "application/json", nil)
	if !errors.Is(err, ErrPrivateTarget) {
		t.Fatalf("loopback delivery: got %v, want ErrPrivateTarget", err)
	}

	allowed, _, _ := newWebhookClient(2*time.Second, true)
	resp, err := allowed.Post(srv.URL, "application/json", nil)
	if err != nil {
		t.Fatalf("WEBHOOK_ALLOW_PRIVATE delivery: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
}

// With an outbound proxy the only thing we dial is the proxy, which is
// routinely on a private address and must not be blocked. Targets the
// proxy config excludes still go direct and still get checked.
func TestWebhookClientHonoursProxy(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "hooks.example.invalid" {
			proxied.Add(1)
		}
		w.WriteHeader(204)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")

	client, _, _ := newWebhookClient(2*time.Second, false)
	resp, err := client.Post("http://hooks.example.invalid/hook", "application/json", nil)
	if err != nil {
		t.Fatalf("delivery via proxy: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 || proxied.Load() != 1 {
		t.Fatalf("status=%d proxied=%d, want 204 via proxy", resp.StatusCode, proxied.Load())
	}

	// Loopback targets bypass the proxy per httpproxy rules, so this
	// dial is direct and the guard must still fire. A separate server:
	// the proxy's own address is exempt by design.
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer direct.Close()
	_, err = client.Post(direct.URL+"/hook", "application/json", nil)
	if !errors.Is(err, ErrPrivateTarget) {
		t.Fatalf("direct loopback with proxy set: got %v, want ErrPrivateTarget", err)
	}
}

// Through a proxy the target is never dialed here, so the pre-send
// guard has to classify it itself. Direct targets are left to the
// dialer.
func TestGuardTargetBehindProxy(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.internal:3128")
	viaProxy := &WebhookService{proxyFor: func(*url.URL) (*url.URL, error) { return proxyURL, nil }}
	direct := &WebhookService{proxyFor: func(*url.URL) (*url.URL, error) { return nil, nil }}
	ctx := context.Background()

	for _, raw := range []string{"http://localhost/h", "http://127.0.0.1/h", "http://100.100.100.200/h", "http://[fd00::1]/h"} {
		u, _ := url.Parse(raw)
		if err := viaProxy.guardTarget(ctx, u); !errors.Is(err, ErrPrivateTarget) {
			t.Errorf("proxied %s: got %v, want ErrPrivateTarget", raw, err)
		}
		if err := direct.guardTarget(ctx, u); err != nil {
			t.Errorf("direct %s: pre-send guard should defer to the dialer, got %v", raw, err)
		}
	}
	u, _ := url.Parse("http://8.8.8.8/h")
	if err := viaProxy.guardTarget(ctx, u); err != nil {
		t.Errorf("proxied public literal: %v", err)
	}
	lax := &WebhookService{allowPrivate: true, proxyFor: viaProxy.proxyFor}
	u, _ = url.Parse("http://127.0.0.1/h")
	if err := lax.guardTarget(ctx, u); err != nil {
		t.Errorf("allowPrivate: %v", err)
	}
}

// A public receiver behind a proxy redirects to the metadata service:
// the proxy would happily follow, so the redirect hop must be
// classified before it is followed.
func TestWebhookRedirectBehindProxyIsGuarded(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
			return
		}
		w.WriteHeader(204)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")

	svc := NewWebhookService(nil, slog.Default(), 2*time.Second, 1, false)
	_, err := svc.client.Post("http://hooks.example.invalid/redir", "application/json", nil)
	if !errors.Is(err, ErrPrivateTarget) {
		t.Fatalf("redirect to metadata via proxy: got %v, want ErrPrivateTarget", err)
	}
	resp, err := svc.client.Post("http://hooks.example.invalid/ok", "application/json", nil)
	if err != nil {
		t.Fatalf("plain proxied delivery: %v", err)
	}
	resp.Body.Close()
}

// The dialer exempts the proxy's own host:port so proxied traffic can
// reach it. A webhook aimed directly at that address (NO_PROXY, or the
// proxy host simply being the target) must still be classified.
func TestDirectRequestToProxyAddressIsGuarded(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "127.0.0.1")

	svc := NewWebhookService(nil, slog.Default(), 2*time.Second, 1, false)
	u, _ := url.Parse(proxy.URL + "/hook")
	if p, _ := svc.proxyFor(u); p != nil {
		t.Fatalf("test setup: expected a direct request, got proxy %v", p)
	}
	if err := svc.guardTarget(context.Background(), u); !errors.Is(err, ErrPrivateTarget) {
		t.Fatalf("direct request to the proxy's own address: got %v, want ErrPrivateTarget", err)
	}
	// The dialer alone would have let this through — that is exactly
	// the gap the pre-send guard closes.
	resp, err := svc.client.Post(proxy.URL+"/hook", "application/json", nil)
	if err == nil {
		resp.Body.Close()
	}
	if err != nil && !errors.Is(err, ErrPrivateTarget) {
		t.Fatalf("unexpected dial error: %v", err)
	}
}

// Behind a proxy an unresolvable host is refused with guidance, not
// handed to the proxy on trust.
func TestGuardTargetUnresolvableBehindProxy(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.internal:3128")
	svc := &WebhookService{proxyFor: func(*url.URL) (*url.URL, error) { return proxyURL, nil }}
	u, _ := url.Parse("http://no-such-host.invalid/hook")
	err := svc.guardTarget(context.Background(), u)
	if !errors.Is(err, ErrUnresolvableTarget) {
		t.Fatalf("got %v, want ErrUnresolvableTarget", err)
	}
	if !strings.Contains(err.Error(), "WEBHOOK_ALLOW_PRIVATE") {
		t.Fatalf("error should tell the operator what to set: %v", err)
	}
}

// rawProxy is a one-shot HTTP proxy that records the request line and
// Host header exactly as written on the wire. net/http's server cannot
// be used for this: it overwrites r.Host with the absolute-URI host.
func rawProxy(t *testing.T) (addr string, got func() (line, host string)) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var line, host string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				l, _ := r.ReadString('\n')
				var h string
				for {
					hdr, err := r.ReadString('\n')
					if err != nil || hdr == "\r\n" {
						break
					}
					if strings.HasPrefix(strings.ToLower(hdr), "host:") {
						h = strings.TrimSpace(hdr[5:])
					}
				}
				mu.Lock()
				line, host = strings.TrimSpace(l), h
				mu.Unlock()
				_, _ = c.Write([]byte("HTTP/1.1 204 No Content\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
			}(c)
		}
	}()
	return "http://" + ln.Addr().String(), func() (string, string) { mu.Lock(); defer mu.Unlock(); return line, host }
}

// Proxied deliveries must not let the proxy resolve the hostname: the
// request line carries the address we validated, the Host header the
// original name, and a split-horizon answer never reaches the proxy.
func TestProxiedDeliveryPinsValidatedAddress(t *testing.T) {
	proxyURL, seen := rawProxy(t)
	t.Setenv("HTTP_PROXY", proxyURL)
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")

	svc := NewWebhookService(nil, slog.Default(), 2*time.Second, 1, false)
	svc.lookup = func(_ context.Context, host string) ([]net.IPAddr, error) {
		switch host {
		case "hooks.example.test":
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		case "evil.example.test":
			// Public to us, metadata to the proxy — the classic split
			// horizon. Pinning makes the proxy's view irrelevant.
			return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}

	req, _ := http.NewRequest("POST", "http://hooks.example.test:8080/hook?x=1", nil)
	resp, err := svc.send(context.Background(), req)
	if err != nil {
		t.Fatalf("pinned proxied delivery: %v", err)
	}
	resp.Body.Close()
	line, host := seen()
	if line != "POST http://93.184.216.34:8080/hook?x=1 HTTP/1.1" {
		t.Errorf("request line = %q, want the validated IP in the absolute URI", line)
	}
	if host != "hooks.example.test:8080" {
		t.Errorf("Host header = %q, want the original hostname", host)
	}

	req, _ = http.NewRequest("POST", "http://evil.example.test/hook", nil)
	if _, err := svc.send(context.Background(), req); !errors.Is(err, ErrPrivateTarget) {
		t.Fatalf("private resolution: got %v, want ErrPrivateTarget", err)
	}
}

// On the pinned path a redirect is the receiver's answer, not followed:
// each hop would need its own pinning against a different host.
func TestProxiedDeliveryDoesNotFollowRedirects(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/", http.StatusFound)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")
	svc := NewWebhookService(nil, slog.Default(), 2*time.Second, 1, false)
	svc.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	req, _ := http.NewRequest("POST", "http://hooks.example.test/redir", nil)
	resp, err := svc.send(context.Background(), req)
	if err != nil {
		t.Fatalf("redirecting receiver: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302 returned as-is", resp.StatusCode)
	}
}

// An https proxy: its certificate must be checked against the proxy's
// own name, and the target hop must still be pinned. The transport's
// shared TLSClientConfig used to validate the proxy cert against the
// target hostname and fail every delivery.
func TestProxiedDeliveryThroughHTTPSProxy(t *testing.T) {
	var seenLine atomic.Value
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenLine.Store(r.Method + " " + r.URL.String())
		w.WriteHeader(204)
	}))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", proxy.URL) // https://127.0.0.1:port
	t.Setenv("NO_PROXY", "")

	svc := NewWebhookService(nil, slog.Default(), 2*time.Second, 1, false)
	svc.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	roots := x509.NewCertPool()
	roots.AddCert(proxy.Certificate())
	svc.proxyTLS = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}

	req, _ := http.NewRequest("POST", "http://hooks.example.test/hook", nil)
	resp, err := svc.send(context.Background(), req)
	if err != nil {
		t.Fatalf("delivery through https proxy: %v", err)
	}
	resp.Body.Close()
	if got := seenLine.Load(); got != "POST http://93.184.216.34:80/hook" {
		t.Errorf("proxy saw %v, want the pinned absolute URI", got)
	}

	// Wrong roots → the proxy certificate is rejected, not silently
	// accepted and not validated against the target name.
	svc.proxyTLS = &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12}
	if _, err := svc.send(context.Background(), req); err == nil || !strings.Contains(err.Error(), "tls to proxy") {
		t.Fatalf("untrusted proxy cert: got %v, want a proxy TLS error", err)
	}
}

func TestProxyDialAddrs(t *testing.T) {
	got := proxyDialAddrs("http://proxy.internal:3128", "https://egress.corp", "10.0.0.7:8080", "")
	for _, want := range []string{"proxy.internal:3128", "egress.corp:443", "10.0.0.7:8080"} {
		if !got[want] {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("got %d entries, want 3: %v", len(got), got)
	}
}

func TestValidateTarget(t *testing.T) {
	strict := &WebhookService{allowPrivate: false}
	lax := &WebhookService{allowPrivate: true}

	tests := []struct {
		url     string
		private bool
	}{
		{"https://hooks.example.com/keygate", false},
		{"https://hooks.example.com:8443/x", false},
		{"http://localhost:3000/hook", true},
		{"http://127.0.0.1/hook", true},
		{"http://[::1]:8080/hook", true},
		{"http://10.0.0.5/hook", true},
		{"http://192.168.1.20:9000/hook", true},
		{"http://172.16.0.1/hook", true},
		{"http://169.254.169.254/latest/meta-data/", true},
		{"http://0.0.0.0/hook", true},
		{"http://100.100.100.200/latest/meta-data/", true}, // CGNAT: Alibaba metadata
		{"http://100.127.255.254/hook", true},
		{"http://198.18.0.1/hook", true},
		{"http://240.0.0.1/hook", true},
		{"http://[::ffff:127.0.0.1]/hook", true},
		{"http://[64:ff9b::a00:1]/hook", true},
		{"http://[2002:a00:1::1]/hook", true},
		{"http://[2001:db8::1]/hook", true},
		{"http://LOCALHOST/hook", true},
		{"http://100.63.255.255/hook", false}, // just outside CGNAT
		{"http://8.8.8.8/hook", false},
		{"http://[2606:4700::1111]/hook", false},
	}
	for _, tt := range tests {
		err := strict.ValidateTarget(tt.url)
		if tt.private && !errors.Is(err, ErrPrivateTarget) {
			t.Errorf("strict %s: got %v, want ErrPrivateTarget", tt.url, err)
		}
		if !tt.private && err != nil {
			t.Errorf("strict %s: unexpected %v", tt.url, err)
		}
		if err := lax.ValidateTarget(tt.url); err != nil {
			t.Errorf("allowPrivate %s: unexpected %v", tt.url, err)
		}
	}
}
