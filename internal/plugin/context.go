package plugin

import "context"

type pluginIdentityContextKey struct{}

func withPluginIdentity(ctx context.Context, pluginID string) context.Context {
	return context.WithValue(ctx, pluginIdentityContextKey{}, pluginID)
}

// PluginIdentity returns the verified plugin identity attached to a reverse
// RPC context, if one is available.
func PluginIdentity(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(pluginIdentityContextKey{}).(string)
	return value
}
