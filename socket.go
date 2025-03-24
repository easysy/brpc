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

// Socket represents a server that manages plugin connections and communications.
type Socket struct {
	listener net.Listener                            // network listener for incoming connections
	plugins  collector.Collector[string, *processor] // collection of registered plugins
	async    chan *AsyncData                         // channel for async received values
	shutdown atomic.Bool                             // true when Socket is in shutdown
	wg       sync.WaitGroup                          // wg is required to gracefully stop all plugins

	keySequencer func(name string) string
	attempts     uint

	callback func(info *PluginInfo)
	waiters  collector.Collector[string, chan struct{}]
}

// Serve initializes the Socket and starts listen for incoming connections.
func (s *Socket) Serve(listener net.Listener, opts ...Options) {
	s.shutdown.Store(false)

	s.listener = listener
	if s.plugins == nil {
		s.plugins = collector.New[string, *processor]()
	}
	if s.waiters == nil {
		s.waiters = collector.New[string, chan struct{}]()
	}
	s.async = make(chan *AsyncData)

	for _, opt := range opts {
		opt.apply(s)
	}

	go s.serve()
}

// serve listens for incoming connections and handles them in separate goroutines.
func (s *Socket) serve() {
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

// handleConnection manages the connection lifecycle for a plugin.
func (s *Socket) handleConnection(c *codec) {
	defer func() {
		c.close()
		s.wg.Done()
	}()

	// Handle handshake to read plugin information
	info := new(PluginInfo)
	if err := c.read(info); err != nil {
		slog.Error("handshake", "error", err)
		return
	}

	// Initialize the plugin processor
	plug := &processor{PluginInfo: info, codec: c}

	// Handling plugin registration with optional name sequencing
	if !s.plugins.StoreWithKeyResolver(info.Name, plug, s.keySequencer, s.attempts) {
		msg := "handshake (conflict) plugin with the same name is already connected"

		if s.keySequencer != nil {
			msg = "handshake (conflict) unique name not found after attempts"
		}

		slog.Warn(msg, "name", info.Name)

		if err := c.write(&Envelope{Method: MethodShutdown, Error: msg}); err != nil {
			slog.Error("handshake ", "error", err)
		}
		return
	}

	// Finish initializing the plugin processor
	plug.pending = make(map[uint64]*call)
	plug.end = make(chan struct{})

	go s.sendAsync(&AsyncData{Name: info.Name, Payload: info.Version + " connected"})

	// If the plugin has a waiter, let it know that the plugin is connected
	if w, ok := s.waiters.LoadAndDelete(info.Name); ok {
		close(w)
	}

	if !s.shutdown.Load() {
		plug.receive(&s.wg, s.sendAsync) // Start receiving messages for this plugin
	}

	s.plugins.Delete(info.Name)
	go s.sendAsync(&AsyncData{Name: info.Name, Payload: info.Version + " disconnected"})

	if s.callback != nil {
		s.callback(info)
	}
}

// sendAsync sends async data to async channel.
// It skips sending to async channel if there are no readers for a second, to avoid hanging goroutines.
func (s *Socket) sendAsync(a *AsyncData) {
	select {
	case s.async <- a:
	case <-time.NewTimer(time.Second).C:
	}
}

// RegisterCallback registers a callback function that will be called whenever a plugin is disconnected.
func (s *Socket) RegisterCallback(callback func(info *PluginInfo)) {
	s.callback = callback
}

// Call invokes the named method of the specified plugin with the provided payload,
// waits for it to complete, and returns its return and error status.
func (s *Socket) Call(id, name, method string, payload any) (any, error) {
	if plug, ok := s.plugins.Load(name); ok {
		return plug.call(id, method, payload)
	}
	return nil, errors.New("plugin " + name + " not found")
}

type result struct {
	name     string
	response any
	err      error
}

// Broadcast invokes the named method with the provided payload on all running plugins,
// waits for them to complete, and returns their return and error status.
func (s *Socket) Broadcast(id, method string, payload any) (map[string]any, map[string]error) {
	var wg sync.WaitGroup
	res := make(chan result)
	rs, es := make(map[string]any), make(map[string]error)

	s.plugins.Range(func(k string, p *processor) bool {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := p.call(id, method, payload)
			res <- result{name: k, response: r, err: e}
		}()
		return true
	})

	// Collect results
	go func() {
		for r := range res {
			if r.err != nil {
				es[r.name] = r.err
			} else {
				rs[r.name] = r.response
			}
		}
	}()

	wg.Wait()
	close(res)

	return rs, es
}

// Async retrieves the next message from the async channel.
// It returns connection closed error when socket is stopped.
func (s *Socket) Async() (*AsyncData, error) {
	if c := <-s.async; c != nil {
		return c, nil
	}
	return nil, ErrShutdown
}

// WaitFor waits for the specified plugin to connect within a given timeout duration.
// If the plugin connects within the timeout, it returns `true`.
// If the timeout expires before the plugin connects, it returns `false`.
func (s *Socket) WaitFor(name string, timeout time.Duration) bool {
	w := make(chan struct{})
	s.waiters.Store(name, w)

	select {
	case <-w:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Connected returns a list of connected plugins.
// If `full` is true, all Functions are included; otherwise, only Name and Version are returned.
func (s *Socket) Connected(full bool) map[string]*PluginInfo {
	connected := make(map[string]*PluginInfo)

	s.plugins.Range(func(name string, p *processor) bool {
		connected[name] = p.DeepCopy(full)
		return true
	})

	return connected
}

// PluginInfo returns a full plugin info based on its name.
func (s *Socket) PluginInfo(name string) *PluginInfo {
	if p, ok := s.plugins.Load(name); ok {
		return p.DeepCopy(true)
	}
	return nil
}

// Unplug sends a stop request to a plugin based on its name.
// An optional `id` can be provided to trace the request.
func (s *Socket) Unplug(id string, name string) {
	if plug, ok := s.plugins.Load(name); ok && !plug.shutdown.Swap(true) {
		plug.stop(id)
	}
}

// Shutdown gracefully stops all running plugins and stops the Socket.
// An optional `id` can be provided to trace the request.
func (s *Socket) Shutdown(id string) error {
	if s.shutdown.Swap(true) {
		return nil
	}

	s.plugins.Range(func(_ string, p *processor) bool {
		if !p.shutdown.Swap(true) {
			go p.stop(id)
		}
		return true
	})

	s.wg.Wait() // Wait until all plugins are stopped

	return s.listener.Close()
}

type call struct {
	response chan any
	error    error
}

// processor implements the socket-side plugin processor.
type processor struct {
	*PluginInfo

	codec *codec

	mu       sync.Mutex
	seq      uint64
	pending  map[uint64]*call
	shutdown atomic.Bool
	end      chan struct{}
}

func (p *processor) call(trace, method string, payload any) (any, error) {
	if p.shutdown.Load() {
		return nil, ErrShutdown
	}

	e := &Envelope{Trace: trace, Method: method}
	if err := e.encode(payload); err != nil {
		return nil, err
	}

	c := &call{response: make(chan any, 1)}

	p.mu.Lock()
	seq := p.seq
	p.pending[seq] = c
	p.seq++
	p.mu.Unlock()

	e.Seq = seq
	if err := p.codec.write(e); err != nil {
		p.mu.Lock()
		delete(p.pending, seq)
		p.mu.Unlock()

		c.error = err
		close(c.response)
	}

	return <-c.response, c.error
}

func (p *processor) receive(wg *sync.WaitGroup, async func(a *AsyncData)) {
	defer close(p.end)

	for {
		e := new(Envelope)
		if err := p.codec.read(e); err != nil {
			p.mu.Lock()
			for seq, c := range p.pending {
				delete(p.pending, seq)
				c.error = err
				close(c.response)
			}
			p.mu.Unlock()
			return
		}
		wg.Add(1)
		go p.post(wg, async, e)
	}
}

func (p *processor) post(wg *sync.WaitGroup, async func(a *AsyncData), e *Envelope) {
	defer wg.Done()

	if e.Method == MethodAsync {
		a := &AsyncData{Name: p.Name}
		if err := e.decode(&a.Payload); err != nil {
			slog.Error("async payload decode", "error", err)
			return
		}
		go async(a)
		return
	}

	seq := e.Seq

	p.mu.Lock()
	c, ok := p.pending[seq]
	delete(p.pending, seq)
	p.mu.Unlock()

	if !ok {
		slog.Error("no pending call for", "envelop", e)
		return
	}

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

func (p *processor) stop(trace string) {
	if err := p.codec.write(&Envelope{Trace: trace, Method: MethodShutdown}); err != nil {
		slog.Error("shutdown plugin", "name", p.PluginInfo.Name, "version", p.PluginInfo.Version, "error", err)
	}
	<-p.end
}
