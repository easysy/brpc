package brpc

// SocketOption configures a Socket at construction time.
type SocketOption func(*socket)

// WithKeySequencer sets a function to generate a unique plugin name on conflict,
// applied up to attempts times before rejecting the connection.
func WithKeySequencer(fn func(name string) string, attempts uint) SocketOption {
	return func(s *socket) {
		s.keySequencer = fn
		s.attempts = attempts
	}
}

// WithCallback sets fn as the handler called whenever a plugin disconnects.
// graceful is true when the disconnection was requested explicitly via Unplug or Shutdown.
func WithCallback(fn func(info *PluginInfo, graceful bool)) SocketOption {
	return func(s *socket) {
		s.callback = fn
	}
}

// LocalOption configures a Local at construction time.
type LocalOption func(*local)

// WithCtxKey sets the context key used to store the trace ID in each method's context.
func WithCtxKey(key any) LocalOption {
	return func(l *local) {
		l.ctxKey = key
	}
}
