package brpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
)

// Plugin represents a plugin instance that handles communication via a connection.
// It manages a map of registered methods and uses reflection to invoke them based on incoming requests.
type Plugin struct {
	codec *codec
	wg    sync.WaitGroup

	name    string                 // name of plugin
	rec     reflect.Value          // receiver of methods for the plugin
	typ     reflect.Type           // type of the receiver
	methods map[string]*methodType // registered methods
	async   chan any               // optional hook for async processing
	ctxKey  any                    // optional key is used to add a trace ID to the context
}

// Start registers the provided plugin receiver uses reflection to inspect the receiver's methods
// and ensure that they follow the expected signature. It checks for methods that look schematically like:
//
//	func (t *T) MethodName(ctx context.Context, in T1) (out T2, err error)
//
// and / or
//
//	func (t *T) UseAsyncHook(hook chan any)
//
// where T1 and T2 exported (or builtin) types.
//
// Methods that do not match the required signatures are ignored.
//
// If the plugin has at least one valid method it establishes a connection,
// sends a handshake containing plugin information to the socket and enters the listener for incoming requests.
//
// If no valid methods are found, it returns an error.
//
// If the plugin uses the async hook, a goroutine is started to handle hook-related values.
func (p *Plugin) Start(v any, info *PluginInfo, conn io.ReadWriteCloser, ctxKey any) error {
	rec := reflect.ValueOf(v)
	typ := reflect.TypeOf(v)

	name := reflect.Indirect(rec).Type().Name()
	methods := suitableMethods(typ)

	// If no valid methods are found, return an error
	if len(methods) == 0 {
		str := "plugin.Register: type " + name + " has no exported methods of suitable type"

		// To help the user, see if a pointer receiver would work
		if methods = suitableMethods(reflect.PointerTo(typ)); len(methods) != 0 {
			str += " (hint: pass a pointer to value of that type)"
		}

		return errors.New(str)
	}

	p.name = name
	p.rec = rec
	p.typ = typ
	p.methods = methods
	p.ctxKey = ctxKey

	// If the plugin has a hook method named "UseHook", initialize the hook channel and pass it to the hook method
	if m, ok := methods[useAsyncHook]; ok {
		p.async = make(chan any)

		// Call the hook method with the hook channel
		m.method.Func.Call([]reflect.Value{p.rec, reflect.ValueOf(p.async)})
		delete(methods, useAsyncHook)
	}

	p.codec = newCodec(conn)
	defer p.codec.close()

	// Send handshake containing plugin information to the socket
	if err := p.codec.write(info); err != nil {
		return err
	}

	// If the plugin uses async hook, start the async writer in a separate goroutine
	if p.async != nil {
		end := make(chan struct{})
		defer close(end)

		go p.asyncWriter(end)
	}

	slog.Info("plugin started", "name", info.Name, "version", info.Version)
	defer slog.Info("plugin stopped", "name", info.Name, "version", info.Version)

	// Start listening for incoming requests
	return p.listen()
}

// listen waits for incoming envelopes (requests) and processes them in separate goroutines.
func (p *Plugin) listen() (err error) {
	for {
		e := new(Envelope)
		if err = p.codec.read(e); err != nil {
			return
		}

		// If it's a shutdown request, stop the plugin after processing pending requests
		if e.Method == MethodShutdown {
			slog.Info("received shutdown request")
			p.wg.Wait() // Wait for all pending requests to complete
			return
		}

		p.wg.Add(1)
		go p.thread(e)
	}
}

// thread processes an incoming envelope (request) in a separate goroutine.
// It first ensures that the WaitGroup counter is decremented when done,
// allowing the system to track pending operations for graceful shutdowns.
func (p *Plugin) thread(e *Envelope) {
	defer p.wg.Done()

	ctx := context.Background()
	if e.Trace != "" && p.ctxKey != nil {
		ctx = context.WithValue(ctx, p.ctxKey, e.Trace)
	}

	// Process the envelope (execute the requested method) using the plugin's method registry
	if err := p.processor(ctx, e); err != nil {
		e.Error = err.Error()
		e.Payload = nil
	}

	// Send the processed response (or error) back through the socket
	if err := p.codec.write(e); err != nil {
		slog.ErrorContext(ctx, "thread write envelope", "error", err)
	}
}

// processor decodes the request, invokes the correct registered method using reflection,
// and encodes the response back into the envelope.
// It handles any errors that occur during method execution.
func (p *Plugin) processor(ctx context.Context, e *Envelope) error {
	// Retrieve the method associated with the name in the envelope
	m, ok := p.methods[e.Method]
	if !ok {
		return errors.New("method not found")
	}

	// Create a new instance of the input type and decode the Envelope's Payload into it
	in := reflect.New(m.iType).Interface()
	if err := e.decode(&in); err != nil {
		return err
	}

	// Call the method with the decoded input, passing the plugin's receiver and the input
	returnValues := m.method.Func.Call([]reflect.Value{p.rec, reflect.ValueOf(ctx), reflect.ValueOf(in).Elem()})

	// Check if the method returned an error
	if err := returnValues[1]; !err.IsNil() {
		return err.Interface().(error)
	}

	// Encode the method's successful output back into the envelope
	return e.encode(returnValues[0].Interface())
}

// asyncWriter listens for hook-based values on the async channel and sends them through the socket.
// It runs as a separate goroutine to handle values independently of the main listen loop.
func (p *Plugin) asyncWriter(end chan struct{}) {
	slog.Info("async writer started")
	defer slog.Info("async writer stopped")

	e := &Envelope{Method: MethodAsync}

	for {
		select {
		case <-end:
			return
		case payload := <-p.async:
			if payload == nil {
				continue
			}
			if err := e.encode(payload); err != nil {
				slog.Error("async encode", "error", err)
				continue
			}
			if err := p.codec.write(e); err != nil {
				slog.Error("async writer", "error", err)
			}
		}
	}
}
