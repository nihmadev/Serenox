package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
	serenox "github.com/nihmadev/Serenox/sdk/go"
)

type DurableStore interface {
	Get(context.Context, string, string) (json.RawMessage, bool, error)
	Set(context.Context, string, string, json.RawMessage) error
}

type ActionService interface {
	Apply(context.Context, protocol.Action) (protocol.ActionResult, error)
}
type JobService interface {
	Create(context.Context, protocol.JobSpec) (protocol.JobStatus, error)
	Get(context.Context, protocol.JobGetRequest) (protocol.JobStatus, error)
	Delete(context.Context, protocol.JobIdentityRequest) error
}
type HTTPService interface {
	Request(context.Context, protocol.HTTPRequest) (serenox.HTTPResponse, error)
}
type ExitService interface{ Exit(int) }

type Host struct {
	Store   DurableStore
	Actions ActionService
	Jobs    JobService
	HTTP    HTTPService
	Exit    ExitService
}

type sdkHTTPAdapter struct{ client *serenox.HTTPClient }

func (a sdkHTTPAdapter) Request(ctx context.Context, request protocol.HTTPRequest) (serenox.HTTPResponse, error) {
	return a.client.Request(ctx, request)
}

func hostFromContext(ctx context.Context, exit ExitService) (Host, error) {
	services, ok := serenox.ServicesFromContext(ctx)
	if !ok || services.Storage == nil {
		return Host{}, errors.New("host storage service is unavailable")
	}
	host := Host{Store: services.Storage, Actions: services.Actions, Jobs: services.Jobs, Exit: exit}
	if services.HTTP != nil {
		host.HTTP = sdkHTTPAdapter{services.HTTP}
	}
	return host, nil
}

type connector struct {
	Alias, Origin, Prefix   string
	Methods                 map[string]bool
	ReadOnly                bool
	MaxRequest, MaxResponse int
	TimeoutMS               int64
}

func connectorCatalog() map[string]connector {
	return map[string]connector{"demo-status": {
		Alias: "demo-status", Origin: "https://example.com", Prefix: "/", Methods: map[string]bool{"GET": true},
		ReadOnly: true, MaxRequest: 1024, MaxResponse: 4096, TimeoutMS: 3000,
	}}
}

func buildHTTPRequest(step Step, catalog map[string]connector) (protocol.HTTPRequest, connector, error) {
	item, ok := catalog[step.Connector]
	if !ok {
		return protocol.HTTPRequest{}, connector{}, fmt.Errorf("connector %q is not configured", step.Connector)
	}
	if !item.Methods[step.Method] {
		return protocol.HTTPRequest{}, connector{}, fmt.Errorf("method %q is not approved for connector", step.Method)
	}
	if step.Path == "" || step.Path[0] != '/' || len(step.Path) > 128 || strings.Contains(step.Path, "..") {
		return protocol.HTTPRequest{}, connector{}, errors.New("connector path is invalid")
	}
	request := protocol.HTTPRequest{Method: step.Method, URL: item.Origin + item.Prefix + step.Path[1:], MaxResponseBytes: int64(item.MaxResponse), TimeoutMS: item.TimeoutMS}
	return request, item, request.Validate()
}
