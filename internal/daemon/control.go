package daemon

// The daemon control plane is deliberately small and local-only. It lets the
// CLI ask an already running daemon to use the existing transactional plugin
// controller, without exposing an HTTP listener or requiring a restart.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nihmadev/Serenox/internal/eventbus"
)

const (
	controlDirectory = ".serenox"
	controlSocket    = "control.sock"
	controlToken     = "control.token"
	controlVersion   = 1
	controlTimeout   = 10 * time.Minute
	maxControlBody   = 1 << 20
)

// ErrControlUnavailable means that no running daemon accepted the control
// request. Installation can still succeed in this case and be restored on the
// next daemon startup.
var ErrControlUnavailable = errors.New("daemon control plane unavailable")

// ControlSocketPath returns the local control endpoint for a daemon data root.
func ControlSocketPath(dataRoot string) string {
	return filepath.Join(dataRoot, controlDirectory, controlSocket)
}

// ControlTokenPath returns the authentication token path for a daemon data
// root. The token is rotated on every daemon start and is mode 0600.
func ControlTokenPath(dataRoot string) string {
	return filepath.Join(dataRoot, controlDirectory, controlToken)
}

// ControlTokenPathForSocket derives the token path for a custom socket path.
func ControlTokenPathForSocket(socketPath string) string {
	return filepath.Join(filepath.Dir(socketPath), controlToken)
}

type pluginLoader interface {
	Load(context.Context, string) error
}

type controlRequest struct {
	Version        int    `json:"version"`
	Operation      string `json:"operation"`
	Token          string `json:"token"`
	PackagePath    string `json:"package_path"`
	EventID        string `json:"event_id,omitempty"`
	SubscriptionID string `json:"subscription_id,omitempty"`
	State          string `json:"state,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	Confirmed      bool   `json:"confirmed,omitempty"`
}

type controlResponse struct {
	OK          bool              `json:"ok"`
	Error       string            `json:"error,omitempty"`
	ControlBot  *ControlBotStatus `json:"control_bot,omitempty"`
	EventStatus *eventbus.Stats   `json:"event_status,omitempty"`
	Events      []EventSummary    `json:"events,omitempty"`
}

// ControlBotStatus is the non-secret runtime state exposed only over the
// authenticated local daemon socket. It deliberately contains no token, API
// endpoint, update contents, chats, or actor data.
type ControlBotStatus struct {
	Attached     bool   `json:"attached"`
	Running      bool   `json:"running"`
	BotID        int64  `json:"bot_id,omitempty"`
	Username     string `json:"username,omitempty"`
	SavedOffset  int64  `json:"saved_offset,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	RestartCount int    `json:"restart_count,omitempty"`
}

// ControlServer serves authenticated load requests over a mode-restricted
// Unix socket. The ready channel prevents a request racing daemon startup
// from reaching App.RegisterExternal before the application is initialized.
type ControlServer struct {
	loader pluginLoader
	ready  <-chan struct{}
	logger *slog.Logger

	socketPath string
	tokenPath  string
	token      string

	ctx              context.Context
	cancel           context.CancelFunc
	listener         net.Listener
	handlers         sync.WaitGroup
	closeOnce        sync.Once
	cleanupOnce      sync.Once
	controlBotReload func(context.Context) error
	controlBotStatus func() ControlBotStatus
	eventStatus      func(context.Context) (eventbus.Stats, error)
	eventList        func(context.Context, eventbus.DeliveryState, int) ([]eventbus.Record, error)
	eventGet         func(context.Context, string) (eventbus.Record, bool, error)
	eventReplay      func(context.Context, string, string) error
	eventDiscard     func(context.Context, string) error
}

func (s *ControlServer) SetEventOperations(status func(context.Context) (eventbus.Stats, error), list func(context.Context, eventbus.DeliveryState, int) ([]eventbus.Record, error), get func(context.Context, string) (eventbus.Record, bool, error), replay func(context.Context, string, string) error, discard func(context.Context, string) error) {
	s.eventStatus, s.eventList, s.eventReplay, s.eventDiscard = status, list, replay, discard
	s.eventGet = get
}

func NewControlServer(dataRoot string, loader pluginLoader, ready <-chan struct{}) (*ControlServer, error) {
	if loader == nil {
		return nil, errors.New("plugin loader is required")
	}
	if strings.TrimSpace(dataRoot) == "" {
		return nil, errors.New("daemon data root is required")
	}
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve daemon data root: %w", err)
	}
	return &ControlServer{
		loader: loader, ready: ready,
		socketPath: ControlSocketPath(root), tokenPath: ControlTokenPath(root),
	}, nil
}

// SetLogger connects daemon lifecycle logging without making the control
// server depend on the application package.
func (s *ControlServer) SetLogger(logger *slog.Logger) {
	s.logger = logger
}

// SetControlBotReload wires the daemon-owned control-bot lifecycle into the
// already authenticated local control socket. The request carries no Bot API
// token: the daemon rereads its private account-local secret itself.
func (s *ControlServer) SetControlBotReload(handler func(context.Context) error) {
	s.controlBotReload = handler
}

// SetControlBotStatus makes the daemon-owned poller state visible to the
// local CLI. It is intentionally a snapshot callback to keep this socket
// independent of the control-bot implementation and its secrets.
func (s *ControlServer) SetControlBotStatus(handler func() ControlBotStatus) {
	s.controlBotStatus = handler
}

func (s *ControlServer) Start(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	if s.listener != nil {
		return errors.New("daemon control server already started")
	}
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o700); err != nil {
		return fmt.Errorf("create daemon control directory: %w", err)
	}
	if err := prepareControlSocket(s.socketPath); err != nil {
		return err
	}
	if err := s.writeToken(); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		_ = os.Remove(s.tokenPath)
		return fmt.Errorf("listen on daemon control socket: %w", err)
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(s.socketPath)
		_ = os.Remove(s.tokenPath)
		return fmt.Errorf("set daemon control socket permissions: %w", err)
	}
	s.ctx, s.cancel = context.WithCancel(parent)
	s.listener = listener
	go s.serve()
	go func() {
		<-s.ctx.Done()
		_ = s.Close(context.Background())
	}()
	return nil
}

func (s *ControlServer) writeToken() error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate daemon control token: %w", err)
	}
	s.token = hex.EncodeToString(raw)
	temporary, err := os.CreateTemp(filepath.Dir(s.tokenPath), ".control-token-*")
	if err != nil {
		return fmt.Errorf("create daemon control token: %w", err)
	}
	temporaryName := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect daemon control token: %w", err)
	}
	if _, err := io.WriteString(temporary, s.token+"\n"); err != nil {
		return fmt.Errorf("write daemon control token: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close daemon control token: %w", err)
	}
	if err := os.Rename(temporaryName, s.tokenPath); err != nil {
		return fmt.Errorf("install daemon control token: %w", err)
	}
	committed = true
	return nil
}

func prepareControlSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect daemon control socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("daemon control endpoint exists and is not a socket: %q", path)
	}
	connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return fmt.Errorf("daemon control socket is already in use: %q", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale daemon control socket: %w", err)
	}
	return nil
}

func (s *ControlServer) serve() {
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
			}
			continue
		}
		s.handlers.Add(1)
		go func() {
			defer s.handlers.Done()
			s.handle(connection)
		}()
	}
}

func (s *ControlServer) handle(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(controlTimeout))
	var request controlRequest
	decoder := json.NewDecoder(io.LimitReader(connection, maxControlBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		s.writeResponse(connection, controlResponse{Error: "invalid control request"})
		return
	}
	if request.Version != controlVersion ||
		subtle.ConstantTimeCompare([]byte(request.Token), []byte(s.token)) != 1 {
		s.writeResponse(connection, controlResponse{Error: "control request is not authorized"})
		return
	}
	if request.Operation == "control_bot_status" {
		if s.controlBotStatus == nil {
			s.writeResponse(connection, controlResponse{Error: "control bot lifecycle is unavailable"})
			return
		}
		status := s.controlBotStatus()
		s.writeResponse(connection, controlResponse{OK: true, ControlBot: &status})
		return
	}
	if request.Operation == "control_bot_reload" {
		if s.controlBotReload == nil {
			s.writeResponse(connection, controlResponse{Error: "control bot lifecycle is unavailable"})
			return
		}
		if err := s.controlBotReload(s.ctx); err != nil {
			s.writeResponse(connection, controlResponse{Error: "control bot reload failed"})
			return
		}
		s.writeResponse(connection, controlResponse{OK: true})
		return
	}
	if strings.HasPrefix(request.Operation, "events_") {
		s.handleEvents(connection, request)
		return
	}
	if request.Operation != "load" || strings.TrimSpace(request.PackagePath) == "" {
		s.writeResponse(connection, controlResponse{Error: "package_path is required"})
		return
	}
	if s.logger != nil {
		s.logger.Info("plugin hot-load requested", "package", request.PackagePath)
	}
	if s.ready != nil {
		select {
		case <-s.ready:
		case <-s.ctx.Done():
			s.writeResponse(connection, controlResponse{Error: "daemon is shutting down"})
			return
		}
	}
	loadCtx, cancel := context.WithTimeout(s.ctx, controlTimeout)
	err := s.loader.Load(loadCtx, request.PackagePath)
	cancel()
	if err != nil {
		if s.logger != nil {
			s.logger.Error("plugin hot-load failed", "package", request.PackagePath, "error", err)
		}
		s.writeResponse(connection, controlResponse{Error: err.Error()})
		return
	}
	if s.logger != nil {
		s.logger.Info("plugin hot-load completed", "package", request.PackagePath)
	}
	s.writeResponse(connection, controlResponse{OK: true})
}

func (s *ControlServer) writeResponse(connection net.Conn, response controlResponse) {
	_ = json.NewEncoder(connection).Encode(response)
}

// Close stops accepting new control requests and removes the endpoint and
// rotated token. It is safe to call more than once.
func (s *ControlServer) Close(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	s.closeOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.listener != nil {
			_ = s.listener.Close()
		}
	})
	done := make(chan struct{})
	go func() {
		s.handlers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-parent.Done():
	}
	s.cleanupOnce.Do(func() {
		_ = os.Remove(s.socketPath)
		_ = os.Remove(s.tokenPath)
	})
	return nil
}

// LoadPlugin asks a running daemon in dataRoot to verify, install, and start
// packagePath. The package is never executed by the CLI process.
func LoadPlugin(ctx context.Context, dataRoot, packagePath string) error {
	return LoadPluginAt(ctx, ControlSocketPath(dataRoot), ControlTokenPath(dataRoot), packagePath)
}

// LoadPluginAt is the lower-level form used for custom control socket paths.
func LoadPluginAt(ctx context.Context, socketPath, tokenPath, packagePath string) error {
	return callControl(ctx, socketPath, tokenPath, controlRequest{Version: controlVersion, Operation: "load", PackagePath: packagePath})
}

// ReloadControlBot asks the running daemon to reread the account-local secret
// and restart Bot API polling. It is a local authenticated operation; no bot
// token crosses the control socket.
func ReloadControlBot(ctx context.Context, dataRoot string) error {
	return callControl(ctx, ControlSocketPath(dataRoot), ControlTokenPath(dataRoot), controlRequest{Version: controlVersion, Operation: "control_bot_reload"})
}

// ControlBotRuntimeStatus gets the non-secret runtime state from a running
// daemon. Unlike Status read from the local secret store, this confirms that
// polling actually started and reports a stable failure category if it did not.
func ControlBotRuntimeStatus(ctx context.Context, dataRoot string) (ControlBotStatus, error) {
	return controlBotRuntimeStatusAt(ctx, ControlSocketPath(dataRoot), ControlTokenPath(dataRoot))
}

func controlBotRuntimeStatusAt(ctx context.Context, socketPath, tokenPath string) (ControlBotStatus, error) {
	response, err := callControlResponse(ctx, socketPath, tokenPath, controlRequest{Version: controlVersion, Operation: "control_bot_status"})
	if err != nil {
		return ControlBotStatus{}, err
	}
	if response.ControlBot == nil {
		return ControlBotStatus{}, errors.New("daemon returned no control-bot status")
	}
	return *response.ControlBot, nil
}

func callControl(ctx context.Context, socketPath, tokenPath string, request controlRequest) error {
	_, err := callControlResponse(ctx, socketPath, tokenPath, request)
	return err
}

func callControlResponse(ctx context.Context, socketPath, tokenPath string, request controlRequest) (controlResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return controlResponse{}, fmt.Errorf("%w: read daemon control token %q: %v; is the daemon running?", ErrControlUnavailable, tokenPath, err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if len(token) != 64 {
		return controlResponse{}, errors.New("daemon control token is invalid")
	}
	if _, err := hex.DecodeString(token); err != nil {
		return controlResponse{}, errors.New("daemon control token is invalid")
	}
	if request.Operation == "load" {
		packagePath, err := filepath.Abs(strings.TrimSpace(request.PackagePath))
		if err != nil {
			return controlResponse{}, fmt.Errorf("resolve plugin package: %w", err)
		}
		request.PackagePath = packagePath
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return controlResponse{}, fmt.Errorf("%w: connect to daemon control socket: %v; is the daemon running?", ErrControlUnavailable, err)
	}
	defer connection.Close()
	deadline := time.Now().Add(controlTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	request.Token = token
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return controlResponse{}, fmt.Errorf("send control request: %w", err)
	}
	var response controlResponse
	decoder := json.NewDecoder(io.LimitReader(connection, maxControlBody))
	if err := decoder.Decode(&response); err != nil {
		return controlResponse{}, fmt.Errorf("read control response: %w", err)
	}
	if !response.OK {
		if response.Error == "" {
			response.Error = "daemon rejected hot-load request"
		}
		return controlResponse{}, errors.New(response.Error)
	}
	return response, nil
}
