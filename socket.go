package brpc

import (
	"errors"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/easysy/brpc/collector"
)

// Socket accepts plugin connections over a network listener and dispatches RPC calls to them.
type Socket interface {
	// Call invokes method on the named plugin with payload. id is an optional trace ID.
	Call(id, name, method string, payload any) (any, error)

	// Broadcast calls method on all registered plugins concurrently.
	// Returns two maps keyed by plugin name: successful results and errors.
	Broadcast(id, method string, payload any) (map[string]any, map[string]error)

	// Async blocks until a plugin delivers a notification or the socket shuts down.
	// Returns ErrShutdown after Shutdown is called.
	Async() (*AsyncData, error)

	// Registered returns metadata for all currently connected plugins.
	// full=true includes the Functions map; false returns only Name and Version.
	Registered(full bool) map[string]*PluginInfo

	// PluginInfo returns full metadata for the named plugin, or nil if not connected.
	PluginInfo(name string) *PluginInfo

	// WaitFor blocks until the named plugin connects or timeout elapses.
	WaitFor(name string, timeout time.Duration) bool

	// Unplug sends a graceful shutdown request to the named plugin.
	Unplug(id string, name string)

	// Shutdown gracefully disconnects all plugins and closes the listener.
	Shutdown(id string) error
}

type socket struct {
	base[*socketProcessor]

	listener     net.Listener
	keySequencer func(name string) string
	attempts     uint
	waiters      collector.Collector[string, chan struct{}]
	callback     func(info *PluginInfo, graceful bool)
}

// NewSocket creates a Socket, applies opts, and begins accepting connections from listener.
func NewSocket(listener net.Listener, opts ...SocketOption) Socket {
	s := &socket{}
	s.listener = listener
	s.plugins = collector.New[string, *socketProcessor]()
	s.waiters = collector.New[string, chan struct{}]()
	s.async = make(chan *AsyncData)

	for _, opt := range opts {
		opt(s)
	}

	go s.serve()
	return s
}

func (s *socket) serve() {
	slog.Info("socket started")
	defer slog.Info("socket stopped")

	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, syscall.SIGINT, syscall.SIGTERM)
	conn := make(chan net.Conn)

	go func() {
		for {
			c, err := s.listener.Accept()
			if err != nil {
				signal.Stop(sigint)
				close(sigint)
				return
			}
			conn <- c
		}
	}()

LOOP:
	for {
		select {
		case <-sigint:
			break LOOP
		case c := <-conn:
			s.wg.Add(1)
			go s.handleConnection(newCodec(c))
		}
	}

	if err := s.Shutdown(""); err != nil {
		slog.Error("shutdown socket", "error", err)
	}

	close(s.async)
}

func (s *socket) handleConnection(c *codec) {
	defer func() {
		c.close()
		s.wg.Done()
	}()

	info := new(PluginInfo)
	if err := c.read(info); err != nil {
		slog.Error("handshake", "error", err)
		return
	}

	plug := &socketProcessor{PluginInfo: info, codec: c}

	if !s.plugins.StoreWithKeyResolver(info.Name, plug, s.keySequencer, s.attempts) {
		msg := "handshake (conflict) plugin with the same name is already connected"
		if s.keySequencer != nil {
			msg = "handshake (conflict) unique name not found after attempts"
		}

		slog.Warn(msg, "name", info.Name)

		if err := c.write(&Envelope{Method: MethodShutdown, Error: msg}); err != nil {
			slog.Error("handshake", "error", err)
		}
		return
	}

	plug.pending = collector.New[uint64, *call]()
	plug.end = make(chan struct{})

	s.wg.Add(1)
	go s.sendAsync(&AsyncData{Name: info.Name, Payload: info.Version + " connected"})

	if w, ok := s.waiters.LoadAndDelete(info.Name); ok {
		close(w)
	}

	if !s.shutdown.Load() {
		plug.receive(&s.wg, s.sendAsync)
	}

	s.plugins.Delete(info.Name)

	s.wg.Add(1)
	go s.sendAsync(&AsyncData{Name: info.Name, Payload: info.Version + " disconnected"})

	if s.callback != nil {
		s.callback(info, plug.shutdown.Load())
	}
}

func (s *socket) WaitFor(name string, timeout time.Duration) bool {
	w := make(chan struct{})
	s.waiters.Store(name, w)

	select {
	case <-w:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (s *socket) Unplug(id string, name string) {
	if plug, ok := s.plugins.Load(name); ok && !plug.shutdown.Swap(true) {
		plug.stop(id)
	}
}

func (s *socket) Shutdown(id string) error {
	if s.shutdown.Swap(true) {
		return nil
	}

	s.plugins.Range(func(_ string, p *socketProcessor) bool {
		if !p.shutdown.Swap(true) {
			go p.stop(id)
		}
		return true
	})

	s.wg.Wait()

	return s.listener.Close()
}

type call struct {
	response chan any
	error    error
}

type socketProcessor struct {
	*PluginInfo

	codec    *codec
	seq      atomic.Uint64
	pending  collector.Collector[uint64, *call]
	shutdown atomic.Bool
	end      chan struct{}
}

func (p *socketProcessor) call(trace, method string, payload any) (any, error) {
	if p.shutdown.Load() {
		return nil, ErrShutdown
	}

	e := &Envelope{Trace: trace, Method: method}
	if err := e.encode(payload); err != nil {
		return nil, err
	}

	e.Seq = p.seq.Load()
	p.seq.Add(1)

	c := &call{response: make(chan any)}
	p.pending.Store(e.Seq, c)

	if err := p.codec.write(e); err != nil {
		p.pending.Delete(e.Seq)
		return nil, err
	}

	return <-c.response, c.error
}

func (p *socketProcessor) receive(wg *sync.WaitGroup, async func(a *AsyncData)) {
	defer close(p.end)

	for {
		e := new(Envelope)
		if err := p.codec.read(e); err != nil {
			p.pending.Range(func(u uint64, c *call) bool {
				c.error = err
				close(c.response)
				return true
			})
			return
		}

		if e.Method == MethodAsync {
			wg.Add(1)
			go p.async(async, e)
			continue
		}

		c, ok := p.pending.LoadAndDelete(e.Seq)
		if !ok {
			slog.Error("no pending call for", "envelop", e)
			continue
		}

		wg.Add(1)
		go p.post(wg, c, e)
	}
}

func (p *socketProcessor) async(async func(a *AsyncData), e *Envelope) {
	a := &AsyncData{Name: p.Name}
	if err := e.decode(&a.Payload); err != nil {
		slog.Error("async payload decode", "error", err)
		return
	}

	async(a)
}

func (p *socketProcessor) post(wg *sync.WaitGroup, c *call, e *Envelope) {
	defer wg.Done()

	if e.Error != "" {
		c.error = errors.New(e.Error)
	} else {
		var payload any
		if err := e.decode(&payload); err != nil {
			c.error = err
		} else {
			c.response <- payload
		}
	}

	close(c.response)
}

func (p *socketProcessor) stop(trace string) {
	if err := p.codec.write(&Envelope{Trace: trace, Method: MethodShutdown}); err != nil {
		slog.Error("shutdown plugin", "name", p.Name, "version", p.Version, "error", err)
	}
	<-p.end
}
