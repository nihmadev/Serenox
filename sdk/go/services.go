package plugin

import "context"

type servicesContextKey struct{}

// Services are host capabilities made available to a plugin dispatch. The
// values contain only transport-neutral SDK clients.
type Services struct {
	Actions    *ActionClient
	Storage    *StorageClient
	Media      *MediaClient
	HTTP       *HTTPClient
	Jobs       *JobsClient
	Client     *Client
	Moderation *ModerationClient
	Events     *EventClient
}

func EventsFromContext(ctx context.Context) *EventClient {
	services, ok := ServicesFromContext(ctx)
	if !ok {
		return nil
	}
	return services.Events
}

// StorageClientFromContext returns the durable, namespaced storage client
// negotiated for the current dispatch or job invocation.
func StorageClientFromContext(ctx context.Context) *StorageClient {
	services, ok := ServicesFromContext(ctx)
	if !ok {
		return nil
	}
	return services.Storage
}

// WithServices attaches bounded host services to a handler context. Hosts and
// tests may use this when composing a handler without exposing Telegram
// library objects.
func WithServices(ctx context.Context, services Services) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, servicesContextKey{}, services)
}

// ServicesFromContext returns the host services attached to ctx.
func ServicesFromContext(ctx context.Context) (Services, bool) {
	if ctx == nil {
		return Services{}, false
	}
	services, ok := ctx.Value(servicesContextKey{}).(Services)
	return services, ok
}

// MediaClientFromContext returns the negotiated media client, if the host
// supplied one for this dispatch.
func MediaClientFromContext(ctx context.Context) *MediaClient {
	services, ok := ServicesFromContext(ctx)
	if !ok {
		return nil
	}
	return services.Media
}

func HTTPClientFromContext(ctx context.Context) *HTTPClient {
	services, ok := ServicesFromContext(ctx)
	if !ok {
		return nil
	}
	return services.HTTP
}

func JobsClientFromContext(ctx context.Context) *JobsClient {
	services, ok := ServicesFromContext(ctx)
	if !ok {
		return nil
	}
	return services.Jobs
}

func ClientFromContext(ctx context.Context) *Client {
	services, ok := ServicesFromContext(ctx)
	if !ok {
		return nil
	}
	return services.Client
}

func ModerationFromContext(ctx context.Context) *ModerationClient {
	services, ok := ServicesFromContext(ctx)
	if !ok {
		return nil
	}
	return services.Moderation
}
