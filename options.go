package brpc

type Options interface {
	apply(*Socket)
}

type keySequencer struct {
	fn       func(name string) string
	attempts uint
}

func (f keySequencer) apply(socket *Socket) {
	socket.keySequencer = f.fn
	socket.attempts = f.attempts
}

// WithKeySequencer returns an option that applies a function to modify repeated plugin names,
// enabling multiple connections with the same base name by generating unique variations.
//
// The `fn` parameter is used to transform the name when a conflict is detected,
// and `attempts` defines the maximum number of times the function will be applied
// before failing to register the plugin.
func WithKeySequencer(fn func(name string) string, attempts uint) Options {
	return keySequencer{fn: fn, attempts: attempts}
}
