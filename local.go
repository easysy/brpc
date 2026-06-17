package brpc

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"

	"github.com/easysy/brpc/collector"
)

// Local provides in-process plugin communication without a network connection.
// Plugins are invoked via reflection, making it suitable for scenarios where
// the host and plugins reside in the same process.
type Local interface {
	// Call invokes method on the named plugin with payload. id is an optional trace ID.
	Call(id, name, method string, payload any) (any, error)

	// Broadcast calls method on all registered plugins concurrently.
	// Returns two maps keyed by plugin name: successful results and errors.
	Broadcast(id, method string, payload any) (map[string]any, map[string]error)

	// Async blocks until a plugin delivers a notification or the host shuts down.
	// Returns ErrShutdown after Shutdown is called.
	Async() (*AsyncData, error)

	// Registered returns metadata for all currently registered plugins.
	// full=true includes the Functions map; false returns only Name and Version.
	Registered(full bool) map[string]*PluginInfo

	// PluginInfo returns full metadata for the named plugin, or nil if not registered.
	PluginInfo(name string) *PluginInfo

	// Shutdown signals all plugins to stop and waits for in-flight operations to complete.
	Shutdown()
}

// Registration pairs a plugin implementation with its metadata.
type Registration struct {
	V    any
	Info *PluginInfo
}

type local struct {
	base[*localProcessor]

	ctxKey any
}

// NewLocal registers all provided plugins and applies opts.
// Plugins with no suitable methods are logged and skipped.
func NewLocal(plugins []Registration, opts ...LocalOption) Local {
	l := &local{}
	l.plugins = collector.New[string, *localProcessor]()
	l.async = make(chan *AsyncData)

	for _, opt := range opts {
		opt(l)
	}

	for _, r := range plugins {
		l.register(r.V, r.Info)
	}

	return l
}

func (s *local) register(v any, info *PluginInfo) {
	rec := reflect.ValueOf(v)
	typ := reflect.TypeOf(v)
	name := reflect.Indirect(rec).Type().Name()
	ms := suitableMethods(typ)

	if len(ms) == 0 {
		hint := ""
		if ms = suitableMethods(reflect.PointerTo(typ)); len(ms) != 0 {
			hint = " (hint: pass a pointer to value of that type)"
		}
		slog.Error("local: skip plugin, no suitable methods", "type", name, "plugin", info.Name, "hint", hint)
		return
	}

	plug := &localProcessor{
		PluginInfo: info,
		rec:        rec,
		ctxKey:     s.ctxKey,
	}

	if m, ok := ms[useAsyncHook]; ok {
		plug.hook = make(chan any)
		m.method.Func.Call([]reflect.Value{plug.rec, reflect.ValueOf(plug.hook)})
		delete(ms, useAsyncHook)

		s.wg.Add(1)
		go s.hookForwarder(info.Name, plug.hook)
	}

	info.Functions = ms.functions(info.Functions)
	plug.methods = ms

	if !s.plugins.StoreWithKeyResolver(info.Name, plug, nil, 0) {
		slog.Error("local: skip plugin, already registered", "plugin", info.Name)
		if plug.hook != nil {
			close(plug.hook)
		}
		return
	}

	s.wg.Add(1)
	go s.sendAsync(&AsyncData{Name: info.Name, Payload: "registered"})
}

// Shutdown signals all plugins to stop and waits for in-flight operations to complete.
func (s *local) Shutdown() {
	if s.shutdown.Swap(true) {
		return
	}

	s.plugins.Range(func(_ string, p *localProcessor) bool {
		if p.hook != nil {
			close(p.hook)
		}
		return true
	})

	s.wg.Wait()
	close(s.async)
}

// hookForwarder forwards hook values to the async channel until hook is closed.
func (s *local) hookForwarder(name string, hook chan any) {
	defer s.wg.Done()
	for payload := range hook {
		if payload == nil {
			continue
		}
		s.wg.Add(1)
		go s.sendAsync(&AsyncData{Name: name, Payload: payload})
	}
}

type localProcessor struct {
	*PluginInfo

	rec     reflect.Value
	methods methods
	ctxKey  any // nil disables trace ID injection into method contexts
	hook    chan any
}

func (p *localProcessor) call(trace, method string, payload any) (any, error) {
	m, ok := p.methods[method]
	if !ok {
		return nil, ErrMethodNotFound
	}

	ctx := context.Background()
	if trace != "" && p.ctxKey != nil {
		ctx = context.WithValue(ctx, p.ctxKey, trace)
	}

	in := reflect.New(m.iType).Interface()
	if payload != nil {
		if reflect.TypeOf(payload) == m.iType {
			reflect.ValueOf(in).Elem().Set(reflect.ValueOf(payload))
		} else {
			b, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			if err = json.Unmarshal(b, in); err != nil {
				return nil, err
			}
		}
	}

	returnValues := m.method.Func.Call([]reflect.Value{p.rec, reflect.ValueOf(ctx), reflect.ValueOf(in).Elem()})

	if err := returnValues[1]; !err.IsNil() {
		return nil, err.Interface().(error)
	}

	return returnValues[0].Interface(), nil
}
