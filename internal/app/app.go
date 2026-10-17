// Package app composes the bounded core and owns startup/shutdown ordering.
package app

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nihmadev/Serenox/internal/builtin"
	"github.com/nihmadev/Serenox/internal/config"
	"github.com/nihmadev/Serenox/internal/controlbot"
	"github.com/nihmadev/Serenox/internal/dispatch"
	"github.com/nihmadev/Serenox/internal/eventbus"
	"github.com/nihmadev/Serenox/internal/executor"
	"github.com/nihmadev/Serenox/internal/httpservice"
	"github.com/nihmadev/Serenox/internal/jobs"
	"github.com/nihmadev/Serenox/internal/modules"
	"github.com/nihmadev/Serenox/internal/observability"
	"github.com/nihmadev/Serenox/internal/ratelimit"
	"github.com/nihmadev/Serenox/internal/storage"
	"github.com/nihmadev/Serenox/internal/telegram"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type Options struct {
	Config                config.Runtime
	SelfID                int64
	Transport             telegram.Transport
	Store                 storage.Store
	Logger                *slog.Logger
	IngressCapacity       int
	CommandCapacity       int
	IngressWorkers        int
	CommandEnqueueTimeout time.Duration
	MaxMetricSeries       int
	HTTPService           *httpservice.Service
	PluginController      builtin.PluginController
	RequestRestart        func(context.Context, bool) error
	// ControlBot is optional and owns only Bot API UI actions. It is never used
	// for user-session actions and plugins are not given this transport.
	ControlBot ControlBotTransport
	// Ready is called once the application has initialized its dispatch and
	// executor runtime and can safely accept control-plane plugin loads.
	Ready func()
}

type ControlBotTransport interface {
	Apply(context.Context, pluginv1.Action) (pluginv1.ActionResult, error)
}

// ExternalDispatch is the transport-neutral call implemented by a supervised
// plugin process. RegisterExternal wraps it in the same bounded executor used
// by built-ins; no RPC call is launched directly from Telegram admission.
type ExternalDispatch func(context.Context, pluginv1.DispatchRequest) (pluginv1.DispatchResponse, error)

type preSendHook struct {
	pluginID string
	name     string
	handler  ExternalDispatch
}

// ClientReader is the optional, transport-neutral read surface used by
// host.client. Implementations return protocol DTOs only.
type ClientReader interface {
	GetMe(context.Context) (*pluginv1.Peer, error)
	GetMessage(context.Context, int64, int64) (*pluginv1.Message, error)
	ResolvePeer(context.Context, int64, string) (*pluginv1.Peer, error)
}

type App struct {
	options   Options
	transport telegram.Transport
	store     storage.Store
	ownsStore bool

	parser          atomic.Pointer[dispatch.Parser]
	prefix          atomic.Value // string
	commands        *dispatch.CommandRegistry
	watchers        *dispatch.WatcherRouter
	inlines         *dispatch.InlineRouter
	callbacks       *dispatch.CallbackRouter
	controlCommands *controlbot.Registry
	controlBot      ControlBotTransport
	executor        *executor.Manager
	jobs            *jobs.Scheduler
	limiter         *ratelimit.Limiter
	ingress         *telegram.Ingress
	metrics         *observability.Metrics
	health          *observability.Health
	sampler         *observability.Sampler
	modules         *modules.Registry
	http            *httpservice.Service
	builtins        *builtin.Catalog

	ctx          context.Context
	cancel       context.CancelFunc
	workers      sync.WaitGroup
	pending      sync.WaitGroup
	startedAt    time.Time
	closeOnce    sync.Once
	closeErr     error
	externalMu   sync.Mutex
	jobMu        sync.RWMutex
	preSendMu    sync.RWMutex
	preSendHooks []preSendHook
	jobHandlers  map[string]map[string]pluginv1.JobHandlerSpec
	closing      atomic.Bool
	idempotency  *telegram.IdempotencyStore
	events       *eventbus.Fabric
	profile      *profileLeaseManager
}
