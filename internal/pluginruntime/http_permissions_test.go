package pluginruntime

import (
	"context"
	"testing"

	"github.com/nihmadev/Serenox/internal/pluginpackage"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestHTTPPermissionChecksRunForEveryRequest(t *testing.T) {
	called := 0
	handler := permissionCheckedHTTP(func(context.Context, string, int64, pluginpackage.NetworkPolicy, pluginv1.HTTPRequest) (pluginv1.HTTPResponse, error) {
		called++
		return pluginv1.HTTPResponse{Status: 200, FinalURL: "https://example.test"}, nil
	}, []string{"network.http.read"}, pluginpackage.NetworkPolicy{}, []string{"http"})
	if _, err := handler(context.Background(), "weather", 1, pluginpackage.NetworkPolicy{}, pluginv1.HTTPRequest{Method: "POST", URL: "https://example.test"}); err == nil {
		t.Fatal("POST without write permission unexpectedly succeeded")
	}
	if _, err := handler(context.Background(), "weather", 1, pluginpackage.NetworkPolicy{}, pluginv1.HTTPRequest{Method: "GET", URL: "https://example.test"}); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("handler called %d times, want 1", called)
	}
}
