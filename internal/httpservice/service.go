// Package httpservice owns the only outbound HTTP client available to plugins.
// It deliberately accepts protocol DTOs and verified package policy rather than
// exposing net/http types or a configurable transport to plugin code.
package httpservice

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nihmadev/Serenox/internal/pluginpackage"
	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type Category string

const (
	CategoryInvalidURL     Category = "invalid_url"
	CategoryForbiddenHost  Category = "forbidden_host"
	CategoryDNSFailure     Category = "dns_failure"
	CategoryConnection     Category = "connection_failure"
	CategoryTLS            Category = "tls_failure"
	CategoryTimeout        Category = "timeout"
	CategoryCancellation   Category = "cancellation"
	CategoryRedirect       Category = "redirect_rejected"
	CategoryRequestSize    Category = "request_size_limit"
	CategoryResponseSize   Category = "response_size_limit"
	CategoryHeaderLimit    Category = "header_limit"
	CategoryRateLimit      Category = "rate_limit"
	CategoryOverload       Category = "overload"
	CategoryUpstreamStatus Category = "upstream_http_status"
	CategorySSRF           Category = "ssrf_rejected"
	CategoryUnsupported    Category = "unsupported"
)

type HTTPError struct {
	Category  Category
	Message   string
	Response  *protocol.HTTPResponse
	Retryable bool
}

func (e *HTTPError) Error() string { return string(e.Category) + ": " + e.Message }

type Config struct {
	MaxRequestBytes   int64
	MaxResponseBytes  int64
	MaxHeaders        int
	MaxHeaderBytes    int
	MaxConcurrent     int
	QueueCapacity     int
	RequestsPerSecond float64
	Burst             int
	Resolver          func(context.Context, string) ([]net.IP, error)
	Dial              func(context.Context, string, string) (net.Conn, error)
	Now               func() time.Time
}

type Service struct {
	cfg     Config
	queue   chan struct{}
	mu      sync.Mutex
	plugins map[string]*pluginState
	stats   serviceStats
}

type serviceStats struct {
	requests, completed, failed, timeouts, cancellations, policyRejected, rateLimited, overloaded atomic.Uint64
	bytesSent, bytesReceived                                                                      atomic.Uint64
}

// Stats is a bounded, label-free snapshot suitable for core observability.
type Stats struct {
	Requests, Completed, Failed, Timeouts, Cancellations, PolicyRejected, RateLimited, Overloaded uint64
	BytesSent, BytesReceived                                                                      uint64
}

type pluginState struct {
	active chan struct{}
	tokens float64
	last   time.Time
	budget int64
	used   int64
}

func New(cfg Config) (*Service, error) {
	if cfg.MaxRequestBytes == 0 {
		cfg.MaxRequestBytes = 512 << 10
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = 512 << 10
	}
	if cfg.MaxHeaders == 0 {
		cfg.MaxHeaders = 64
	}
	if cfg.MaxHeaderBytes == 0 {
		cfg.MaxHeaderBytes = 64 << 10
	}
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = 64
	}
	if cfg.QueueCapacity == 0 {
		cfg.QueueCapacity = 256
	}
	if cfg.RequestsPerSecond == 0 {
		cfg.RequestsPerSecond = 10
	}
	if cfg.Burst == 0 {
		cfg.Burst = 20
	}
	if cfg.MaxRequestBytes < 1 || cfg.MaxRequestBytes > 512<<10 || cfg.MaxResponseBytes < 1 || cfg.MaxResponseBytes > 512<<10 || cfg.MaxHeaders < 1 || cfg.MaxHeaders > 256 || cfg.MaxHeaderBytes < 1024 || cfg.MaxHeaderBytes > 1<<20 || cfg.MaxConcurrent < 1 || cfg.MaxConcurrent > 1024 || cfg.QueueCapacity < 1 || cfg.QueueCapacity > 4096 || cfg.RequestsPerSecond <= 0 || cfg.RequestsPerSecond > 1000 || cfg.Burst < 1 || cfg.Burst > 4096 {
		return nil, errors.New("invalid HTTP service limits")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Resolver == nil {
		cfg.Resolver = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	if cfg.Dial == nil {
		var d net.Dialer
		cfg.Dial = d.DialContext
	}
	return &Service{cfg: cfg, queue: make(chan struct{}, cfg.QueueCapacity), plugins: make(map[string]*pluginState)}, nil
}

func (s *Service) Do(ctx context.Context, pluginID string, policy pluginpackage.NetworkPolicy, request protocol.HTTPRequest) (result protocol.HTTPResponse, resultErr error) {
	s.stats.requests.Add(1)
	defer func() {
		if resultErr == nil {
			s.stats.completed.Add(1)
		} else {
			s.stats.failed.Add(1)
		}
		if len(result.Body) > 0 {
			s.stats.bytesReceived.Add(uint64(len(result.Body)))
		}
		var httpErr *HTTPError
		if errors.As(resultErr, &httpErr) {
			switch httpErr.Category {
			case CategoryTimeout:
				s.stats.timeouts.Add(1)
			case CategoryCancellation:
				s.stats.cancellations.Add(1)
			case CategoryRateLimit:
				s.stats.rateLimited.Add(1)
			case CategoryOverload:
				s.stats.overloaded.Add(1)
			case CategoryForbiddenHost, CategoryInvalidURL, CategorySSRF, CategoryRedirect:
				s.stats.policyRejected.Add(1)
			}
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	if pluginID == "" {
		return protocol.HTTPResponse{}, &HTTPError{Category: CategoryForbiddenHost, Message: "plugin identity is required"}
	}
	if err := request.Validate(); err != nil {
		return protocol.HTTPResponse{}, &HTTPError{Category: CategoryInvalidURL, Message: err.Error()}
	}
	if policy.MaxRequestBytes == 0 {
		policy.MaxRequestBytes = s.cfg.MaxRequestBytes
	}
	if policy.MaxResponseBytes == 0 {
		policy.MaxResponseBytes = s.cfg.MaxResponseBytes
	}
	if policy.MaxConcurrent == 0 {
		policy.MaxConcurrent = minInt(s.cfg.MaxConcurrent, 4)
	}
	if policy.RequestsPerSecond == 0 {
		policy.RequestsPerSecond = s.cfg.RequestsPerSecond
	}
	if policy.Burst == 0 {
		policy.Burst = s.cfg.Burst
	}
	if int64(len(request.Body)) > s.cfg.MaxRequestBytes || (policy.MaxRequestBytes > 0 && int64(len(request.Body)) > policy.MaxRequestBytes) {
		return protocol.HTTPResponse{}, &HTTPError{Category: CategoryRequestSize, Message: "request body exceeds the configured limit"}
	}
	u, err := normalizeURL(request.URL)
	if err != nil {
		return protocol.HTTPResponse{}, &HTTPError{Category: CategoryInvalidURL, Message: err.Error()}
	}
	if u.Scheme == "http" && !policy.AllowHTTP {
		return protocol.HTTPResponse{}, &HTTPError{Category: CategoryForbiddenHost, Message: "cleartext HTTP is disabled"}
	}
	if !originAllowed(policy, u) {
		return protocol.HTTPResponse{}, &HTTPError{Category: CategoryForbiddenHost, Message: "origin is not in the verified allowlist"}
	}
	if !s.admit(ctx, pluginID, policy) {
		return protocol.HTTPResponse{}, &HTTPError{Category: CategoryOverload, Message: "HTTP admission queue is full or expired", Retryable: true}
	}
	defer s.release(pluginID)
	if err := s.rate(ctx, pluginID, policy); err != nil {
		return protocol.HTTPResponse{}, err
	}
	if !s.reserveBytes(pluginID, policy, int64(len(request.Body))) {
		return protocol.HTTPResponse{}, &HTTPError{Category: CategoryRequestSize, Message: "total plugin HTTP byte budget exceeded"}
	}
	s.stats.bytesSent.Add(uint64(len(request.Body)))

	maxResponse := s.cfg.MaxResponseBytes
	if policy.MaxResponseBytes > 0 && policy.MaxResponseBytes < maxResponse {
		maxResponse = policy.MaxResponseBytes
	}
	if request.MaxResponseBytes > 0 && request.MaxResponseBytes < maxResponse {
		maxResponse = request.MaxResponseBytes
	}
	timeout := time.Duration(policy.RequestTimeoutMS) * time.Millisecond
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if request.TimeoutMS > 0 && time.Duration(request.TimeoutMS)*time.Millisecond < timeout {
		timeout = time.Duration(request.TimeoutMS) * time.Millisecond
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		return protocol.HTTPResponse{}, &HTTPError{Category: CategoryTimeout, Message: "request deadline has expired", Retryable: true}
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := s.cfg.Now()

	body := strings.NewReader(string(request.Body))
	httpRequest, err := http.NewRequestWithContext(callCtx, request.Method, u.String(), body)
	if err != nil {
		return protocol.HTTPResponse{}, &HTTPError{Category: CategoryInvalidURL, Message: err.Error()}
	}
	for _, header := range request.Headers {
		name := http.CanonicalHeaderKey(header.Name)
		if strings.EqualFold(name, "authorization") && !policy.AllowAuthorization {
			return protocol.HTTPResponse{}, &HTTPError{Category: CategoryForbiddenHost, Message: "authorization headers are not permitted"}
		}
		if strings.EqualFold(name, "cookie") && !policy.AllowCookies {
			return protocol.HTTPResponse{}, &HTTPError{Category: CategoryForbiddenHost, Message: "cookies are not permitted"}
		}
		if strings.EqualFold(name, "user-agent") && !policy.AllowCustomUserAgent {
			return protocol.HTTPResponse{}, &HTTPError{Category: CategoryForbiddenHost, Message: "custom user agents are not permitted"}
		}
		httpRequest.Header.Add(name, header.Value)
	}
	transport := s.transport(callCtx, policy)
	redirects := 0
	client := &http.Client{Transport: transport, CheckRedirect: s.redirectPolicy(policy, u, &redirects), Timeout: timeout}
	resp, err := client.Do(httpRequest)
	if err != nil {
		return protocol.HTTPResponse{}, classifyError(callCtx, err)
	}
	defer resp.Body.Close()
	result = protocol.HTTPResponse{Status: resp.StatusCode, FinalURL: redactURL(resp.Request.URL), Redirects: redirects, ElapsedMS: time.Since(started).Milliseconds(), ContentType: resp.Header.Get("Content-Type")}
	if resp.Uncompressed || resp.Header.Get("Content-Encoding") != "" {
		return result, &HTTPError{Category: CategoryResponseSize, Message: "compressed response bodies are unsupported", Response: &result}
	}
	if err := copyHeaders(&result, resp.Header, s.cfg.MaxHeaders, s.cfg.MaxHeaderBytes); err != nil {
		result.Rejected = true
		result.Body = nil
		return result, err
	}
	limited := io.LimitReader(resp.Body, maxResponse+1)
	data, readErr := io.ReadAll(limited)
	if readErr != nil {
		return result, classifyError(callCtx, readErr)
	}
	if int64(len(data)) > maxResponse {
		result.Rejected = true
		result.Truncated = true
		return result, &HTTPError{Category: CategoryResponseSize, Message: "response body exceeds the configured limit", Response: &result}
	}
	if !s.reserveBytes(pluginID, policy, int64(len(data))) {
		result.Rejected = true
		result.Body = nil
		return result, &HTTPError{Category: CategoryResponseSize, Message: "total plugin HTTP byte budget exceeded", Response: &result}
	}
	result.Body = data
	if resp.StatusCode >= 400 {
		return result, &HTTPError{Category: CategoryUpstreamStatus, Message: "upstream returned an error status", Response: &result, Retryable: resp.StatusCode == 429 || resp.StatusCode >= 500}
	}
	return result, nil
}

func (s *Service) Stats() Stats {
	return Stats{Requests: s.stats.requests.Load(), Completed: s.stats.completed.Load(), Failed: s.stats.failed.Load(), Timeouts: s.stats.timeouts.Load(), Cancellations: s.stats.cancellations.Load(), PolicyRejected: s.stats.policyRejected.Load(), RateLimited: s.stats.rateLimited.Load(), Overloaded: s.stats.overloaded.Load(), BytesSent: s.stats.bytesSent.Load(), BytesReceived: s.stats.bytesReceived.Load()}
}

func (s *Service) transport(ctx context.Context, policy pluginpackage.NetworkPolicy) *http.Transport {
	connectTimeout := time.Duration(policy.ConnectionTimeoutMS) * time.Millisecond
	if connectTimeout == 0 {
		connectTimeout = 10 * time.Second
	}
	return &http.Transport{Proxy: nil, DisableCompression: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: connectTimeout, ResponseHeaderTimeout: time.Duration(policy.HeaderTimeoutMS) * time.Millisecond, IdleConnTimeout: time.Duration(policy.IdleTimeoutMS) * time.Millisecond, DialContext: func(dialCtx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := s.cfg.Resolver(dialCtx, host)
		if err != nil {
			return nil, &HTTPError{Category: CategoryDNSFailure, Message: "DNS resolution failed", Retryable: true}
		}
		for _, ip := range ips {
			if privateAddress(ip) {
				continue
			}
			connectCtx, cancel := context.WithTimeout(dialCtx, connectTimeout)
			conn, dialErr := s.cfg.Dial(connectCtx, network, net.JoinHostPort(ip.String(), port))
			cancel()
			if dialErr == nil {
				return conn, nil
			}
		}
		return nil, &HTTPError{Category: CategorySSRF, Message: "all resolved addresses are forbidden"}
	}}
}

func (s *Service) redirectPolicy(policy pluginpackage.NetworkPolicy, initial *url.URL, redirects *int) func(*http.Request, []*http.Request) error {
	return func(next *http.Request, previous []*http.Request) error {
		if len(previous) >= policy.MaxRedirects && policy.MaxRedirects >= 0 {
			return &HTTPError{Category: CategoryRedirect, Message: "redirect limit exceeded"}
		}
		*redirects = len(previous) + 1
		if next.URL.Scheme != "http" && next.URL.Scheme != "https" || (next.URL.Scheme == "http" && !policy.AllowHTTP) || !originAllowed(policy, next.URL) {
			return &HTTPError{Category: CategoryRedirect, Message: "redirect target is not allowed"}
		}
		if !sameOrigin(initial, next.URL) && !policy.AllowCrossOriginRedirect {
			return &HTTPError{Category: CategoryRedirect, Message: "cross-origin redirects are disabled"}
		}
		next.Header.Del("Authorization")
		next.Header.Del("Cookie")
		next.Header.Del("Set-Cookie")
		return nil
	}
}

func (s *Service) admit(ctx context.Context, pluginID string, policy pluginpackage.NetworkPolicy) bool {
	select {
	case s.queue <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	s.mu.Lock()
	state := s.plugins[pluginID]
	if state == nil {
		if len(s.plugins) >= 1024 {
			s.mu.Unlock()
			<-s.queue
			return false
		}
		max := policy.MaxConcurrent
		if max == 0 {
			max = 4
		}
		state = &pluginState{active: make(chan struct{}, max), tokens: float64(policy.Burst), last: s.cfg.Now()}
		state.budget = policy.TotalByteBudget
		s.plugins[pluginID] = state
	}
	s.mu.Unlock()
	select {
	case state.active <- struct{}{}:
		return true
	case <-ctx.Done():
		<-s.queue
		return false
	}
}

func (s *Service) release(pluginID string) {
	s.mu.Lock()
	state := s.plugins[pluginID]
	s.mu.Unlock()
	if state != nil {
		<-state.active
	}
	<-s.queue
}

func (s *Service) reserveBytes(pluginID string, policy pluginpackage.NetworkPolicy, amount int64) bool {
	if policy.TotalByteBudget <= 0 || amount <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.plugins[pluginID]
	if state == nil || state.budget <= 0 {
		return true
	}
	if amount > state.budget-state.used {
		return false
	}
	state.used += amount
	return true
}

func (s *Service) rate(ctx context.Context, pluginID string, policy pluginpackage.NetworkPolicy) error {
	s.mu.Lock()
	state := s.plugins[pluginID]
	now := s.cfg.Now()
	elapsed := now.Sub(state.last).Seconds()
	state.tokens = minFloat(float64(policy.Burst)-0, state.tokens+elapsed*policy.RequestsPerSecond)
	state.last = now
	if state.tokens >= 1 {
		state.tokens--
		s.mu.Unlock()
		return nil
	}
	wait := time.Duration((1 - state.tokens) / policy.RequestsPerSecond * float64(time.Second))
	s.mu.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return s.rate(ctx, pluginID, policy)
	case <-ctx.Done():
		return &HTTPError{Category: CategoryRateLimit, Message: "HTTP rate limit exceeded", Retryable: true}
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func normalizeURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.Host == "" {
		return nil, errors.New("URL must be an absolute http or https URL without userinfo or fragments")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("URL scheme must be http or https")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || net.ParseIP(host) != nil || strings.ContainsAny(u.Host, "@\x00\r\n") {
		return nil, errors.New("URL host must be a DNS hostname")
	}
	u.Host = host + ":" + strconv.Itoa(defaultPort(u.Scheme, u.Port()))
	if u.Port() == "" {
		u.Host = host
	}
	return u, nil
}

func defaultPort(scheme, port string) int {
	if port != "" {
		n, _ := strconv.Atoi(port)
		return n
	}
	if scheme == "https" {
		return 443
	}
	return 80
}

func originAllowed(policy pluginpackage.NetworkPolicy, u *url.URL) bool {
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	port := defaultPort(u.Scheme, u.Port())
	for _, origin := range policy.Origins {
		oh := strings.TrimSuffix(strings.ToLower(origin.Host), ".")
		op := origin.Port
		if op == 0 {
			op = defaultPort(origin.Scheme, "")
		}
		if origin.Scheme != u.Scheme || op != port {
			continue
		}
		if host == oh || origin.Subdomains && strings.HasSuffix(host, "."+oh) {
			return true
		}
	}
	return false
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && defaultPort(a.Scheme, a.Port()) == defaultPort(b.Scheme, b.Port())
}

func privateAddress(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	for _, raw := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "fc00::/7"} {
		if netip.MustParsePrefix(raw).Contains(addr) {
			return true
		}
	}
	return false
}

func copyHeaders(result *protocol.HTTPResponse, headers http.Header, max int, maxBytes int) error {
	total := 0
	for name, values := range headers {
		if strings.EqualFold(name, "set-cookie") || strings.EqualFold(name, "authorization") || strings.EqualFold(name, "cookie") {
			continue
		}
		if len(result.Headers)+len(values) > max {
			return &HTTPError{Category: CategoryHeaderLimit, Message: "response header count exceeds the configured limit", Response: result}
		}
		for _, value := range values {
			if len(name)+len(value) > maxBytes || strings.ContainsAny(name+value, "\r\n") {
				return &HTTPError{Category: CategoryHeaderLimit, Message: "response header bytes exceed the configured limit", Response: result}
			}
			total += len(name) + len(value)
			result.Headers = append(result.Headers, protocol.HTTPHeader{Name: name, Value: value})
		}
	}
	_ = total
	return nil
}

func classifyError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return &HTTPError{Category: CategoryTimeout, Message: "HTTP request timed out", Retryable: true}
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return &HTTPError{Category: CategoryCancellation, Message: "HTTP request canceled"}
	}
	var certificateError *tls.CertificateVerificationError
	if errors.As(err, &certificateError) {
		return &HTTPError{Category: CategoryTLS, Message: "TLS certificate verification failed"}
	}
	var he *HTTPError
	if errors.As(err, &he) {
		return he
	}
	return &HTTPError{Category: CategoryConnection, Message: "HTTP connection failed", Retryable: true}
}

func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	copy := *u
	copy.RawQuery = ""
	return copy.String()
}
