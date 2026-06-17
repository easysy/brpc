package brpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"reflect"
	"runtime/debug"
	"sync"
	"syscall"
)

// Plugin connects to a Socket and exposes a receiver's methods as RPC endpoints.
type Plugin struct {
	codec *codec
	wg    sync.WaitGroup
	awg   sync.WaitGroup // separate WaitGroup for async writer to avoid blocking shutdown

	name    string
	rec     reflect.Value
	typ     reflect.Type
	methods methods
	async   chan any
	ctxKey  any
}

// Start registers v as the plugin's method receiver, performs a handshake with the Socket,
// and enters the request-processing loop until the connection closes or a signal is received.
//
// v must be a pointer to a struct with at least one exported method matching:
//
//	func (t *T) Method(ctx context.Context, in T1) (T2, error)
//
// and optionally:
//
//	func (t *T) UseAsyncHook(hook chan any)
//
// info provides the plugin's Name, Version, and optional per-function descriptions sent to the Socket.
// ctxKey is stored in each method's context to carry the trace ID; pass nil to disable tracing.
func (p *Plugin) Start(v any, info *PluginInfo, conn io.ReadWriteCloser, ctxKey any) error {
	rec := reflect.ValueOf(v)
	typ := reflect.TypeOf(v)
	name := reflect.Indirect(rec).Type().Name()
	ms := suitableMethods(typ)

	if len(ms) == 0 {
		str := "plugin.Register: type " + name + " has no exported methods of suitable type"
		if ms = suitableMethods(reflect.PointerTo(typ)); len(ms) != 0 {
			str += " (hint: pass a pointer to value of that type)"
		}
		return errors.New(str)
	}

	p.name = name
	p.rec = rec
	p.typ = typ
	p.ctxKey = ctxKey

	if m, ok := ms[useAsyncHook]; ok {
		p.async = make(chan any)
		m.method.Func.Call([]reflect.Value{p.rec, reflect.ValueOf(p.async)})
		delete(ms, useAsyncHook)
	}

	p.codec = newCodec(conn)
	defer func() {
		p.awg.Wait()
		p.codec.close()
	}()

	info.Functions = ms.functions(info.Functions)
	p.methods = ms

	if err := p.codec.write(info); err != nil {
		return err
	}

	if p.async != nil {
		end := make(chan struct{})
		defer close(end)

		p.awg.Add(1)
		go p.asyncWriter(end)
	}

	slog.Info("plugin started", "name", info.Name, "version", info.Version)
	defer slog.Info("plugin stopped", "name", info.Name, "version", info.Version)

	return p.listen()
}

func (p *Plugin) listen() (err error) {
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, syscall.SIGINT, syscall.SIGTERM)
	envelope := make(chan *Envelope)

	go func() {
		for {
			e := new(Envelope)
			if err = p.codec.read(e); err != nil {
				signal.Stop(sigint)
				close(sigint)
				return
			}
			envelope <- e
		}
	}()

	for {
		var e *Envelope
		var shutdown bool

		select {
		case <-sigint:
			shutdown = true
		case e = <-envelope:
			shutdown = e.Method == MethodShutdown
			if e.Error != "" {
				slog.Error(e.Error)
			}
		}

		if shutdown {
			slog.Info("stop the plugin")
			p.wg.Wait()
			return
		}

		p.wg.Add(1)
		go p.thread(e)
	}
}

// thread recovers from panics and writes the result or error back to the socket.
func (p *Plugin) thread(e *Envelope) {
	ctx := context.Background()

	defer p.wg.Done()

	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("panic", "recover", rec, "stack", string(debug.Stack()))
			e.Error = fmt.Sprintf("panic: %v", rec)
		}

		if err := p.codec.write(e); err != nil {
			slog.ErrorContext(ctx, "thread write envelope", "error", err)
		}
	}()

	if e.Trace != "" && p.ctxKey != nil {
		ctx = context.WithValue(ctx, p.ctxKey, e.Trace)
	}

	if err := p.processor(ctx, e); err != nil {
		e.Error = err.Error()
		e.Payload = nil
	}
}

func (p *Plugin) processor(ctx context.Context, e *Envelope) error {
	m, ok := p.methods[e.Method]
	if !ok {
		return ErrMethodNotFound
	}

	in := reflect.New(m.iType).Interface()
	if err := e.decode(in); err != nil {
		return err
	}

	returnValues := m.method.Func.Call([]reflect.Value{p.rec, reflect.ValueOf(ctx), reflect.ValueOf(in).Elem()})

	if err := returnValues[1]; !err.IsNil() {
		return err.Interface().(error)
	}

	return e.encode(returnValues[0].Interface())
}

// asyncWriter reads from p.async and writes each value as an async envelope.
// Stops when end is closed by Start's deferred call.
func (p *Plugin) asyncWriter(end chan struct{}) {
	slog.Info("async writer started")
	defer func() {
		slog.Info("async writer stopped")
		p.awg.Done()
	}()

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
