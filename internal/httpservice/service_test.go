package httpservice

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nihmadev/Serenox/internal/pluginpackage"
	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func fixtureService(t *testing.T, handler http.Handler) (*Service, pluginpackage.NetworkPolicy) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	service, err := New(Config{
		Resolver: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("93.184.216.34")}, nil },
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, pluginpackage.NetworkPolicy{Origins: []pluginpackage.NetworkOrigin{{Scheme: "http", Host: "example.com"}}, AllowHTTP: true, MaxRedirects: 2, MaxResponseBytes: 1024, RequestsPerSecond: 100, Burst: 10}
}

func TestServiceRejectsUnsafeURLsAndPrivateResolution(t *testing.T) {
	service, policy := fixtureService(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, raw := range []string{"https://example.com/a#fragment", "https://user:pass@example.com/", "file:///tmp/x", "https://127.0.0.1/"} {
		_, err := service.Do(context.Background(), "weather", policy, protocol.HTTPRequest{Method: "GET", URL: raw})
		if err == nil {
			t.Errorf("URL %q unexpectedly succeeded", raw)
		}
	}
	privatePolicy := policy
	privatePolicy.Origins = []pluginpackage.NetworkOrigin{{Scheme: "http", Host: "example.com"}}
	service.cfg.Resolver = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	_, err := service.Do(context.Background(), "weather", privatePolicy, protocol.HTTPRequest{Method: "GET", URL: "http://example.com"})
	if err == nil || !strings.Contains(err.Error(), string(CategorySSRF)) {
		t.Fatalf("private resolution error = %v", err)
	}
}

func TestServiceBoundsResponseAndRedirects(t *testing.T) {
	service, policy := fixtureService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://example.com/body", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat("x", 2048)))
	}))
	policy.MaxResponseBytes = 32
	result, err := service.Do(context.Background(), "weather", policy, protocol.HTTPRequest{Method: "GET", URL: "http://example.com/body"})
	if err == nil || !result.Truncated || !result.Rejected {
		t.Fatalf("oversized result=%#v err=%v", result, err)
	}
	policy.MaxResponseBytes = 1024
	policy.MaxRedirects = 1
	_, err = service.Do(context.Background(), "weather", policy, protocol.HTTPRequest{Method: "GET", URL: "http://example.com/redirect"})
	if err == nil {
		t.Fatal("redirect unexpectedly succeeded")
	}
}

func TestServiceCancelsAdmissionAndDoesNotUseProxy(t *testing.T) {
	service, policy := fixtureService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(time.Second) }))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := service.Do(ctx, "weather", policy, protocol.HTTPRequest{Method: "GET", URL: "http://example.com"})
	if err == nil {
		t.Fatal("expired request unexpectedly succeeded")
	}
}
