package manifest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ServiceState string

const (
	ServiceReady       ServiceState = "ready"
	ServiceDegraded    ServiceState = "degraded"
	ServiceUnreachable ServiceState = "unreachable"
	ServiceUnsupported ServiceState = "unsupported"
)

type ServiceStatus struct {
	Name      string       `json:"name"`
	Kind      string       `json:"kind"`
	Required  bool         `json:"required"`
	State     ServiceState `json:"state"`
	Detail    string       `json:"detail,omitempty"`
	CheckedAt time.Time    `json:"checked_at"`
}

type ServiceProbe interface {
	Probe(context.Context, Service) error
}

type Registry struct {
	probes map[string]ServiceProbe
}

func NewRegistry() *Registry { return &Registry{probes: make(map[string]ServiceProbe)} }

func (r *Registry) SetProbe(kind string, probe ServiceProbe) {
	if r == nil || probe == nil {
		return
	}
	r.probes[kind] = probe
}

func (r *Registry) Check(ctx context.Context, doc Document) []ServiceStatus {
	if ctx == nil {
		ctx = context.Background()
	}
	statuses := make([]ServiceStatus, 0, len(doc.Services))
	for name, service := range doc.Services {
		status := ServiceStatus{Name: name, Kind: service.Kind, Required: service.Required, CheckedAt: time.Now().UTC()}
		if probe := r.probes[service.Kind]; probe != nil {
			if err := probe.Probe(ctx, service); err != nil {
				status.State, status.Detail = ServiceUnreachable, safeDetail(err)
			} else {
				status.State = ServiceReady
			}
		} else if service.Kind == "file" {
			status.State = ServiceReady
		} else {
			status.State = ServiceUnsupported
			status.Detail = "typed adapter is not enabled in this build"
		}
		statuses = append(statuses, status)
	}
	sortStatuses(statuses)
	return statuses
}

func (r *Registry) CheckDeclared(ctx context.Context, doc Document) ([]ServiceStatus, error) {
	statuses := r.Check(ctx, doc)
	for _, status := range statuses {
		if status.Required && status.State != ServiceReady {
			return statuses, fmt.Errorf("required service %q is %s: %s", status.Name, status.State, status.Detail)
		}
	}
	return statuses, nil
}

type TCPProbe struct{}

func (TCPProbe) Probe(ctx context.Context, service Service) error {
	address, err := serviceAddress(service)
	if err != nil {
		return err
	}
	timeout := service.ConnectTimeout.Duration
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	if timeout < 100*time.Millisecond || timeout > 30*time.Second {
		return errors.New("service connect timeout is outside 100ms..30s")
	}
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return connection.Close()
}

func serviceAddress(service Service) (string, error) {
	parsed, err := url.Parse(service.Endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("service endpoint must be a URL without credentials, query, or fragment")
	}
	if service.Kind == "postgres" && parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", errors.New("postgres service endpoint must use postgres:// or postgresql://")
	}
	if service.Kind == "redis" && parsed.Scheme != "redis" && parsed.Scheme != "rediss" {
		return "", errors.New("redis service endpoint must use redis:// or rediss://")
	}
	if service.Kind == "object_storage" && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("object storage endpoint must use http:// or https://")
	}
	port := parsed.Port()
	if port == "" {
		switch parsed.Scheme {
		case "postgres", "postgresql":
			port = "5432"
		case "redis", "rediss":
			port = "6379"
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	if number, parseErr := strconv.Atoi(port); parseErr != nil || number < 1 || number > 65535 {
		return "", errors.New("service endpoint port must be between 1 and 65535")
	}
	return net.JoinHostPort(parsed.Hostname(), port), nil
}

func sortStatuses(statuses []ServiceStatus) {
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
}

func safeDetail(err error) string {
	value := strings.TrimSpace(err.Error())
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}
