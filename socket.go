package brpc

import (
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	"github.com/easysy/brpc/collector"
)

// Socket represents a server that manages plugin connections and communications.
type Socket struct {
	listener net.Listener                            // network listener for incoming connections
	plugins  collector.Collector[string, *processor] // collection of registered plugins
	async    chan *AsyncData                         // channel for async received values
	shutdown atomic.Bool                             // true when Socket is in shutdown
	waiter   sync.WaitGroup                          // waiter is required to gracefully stop all plugins
}

// Serve initializes the Socket and starts listen for incoming connections.
func (s *Socket) Serve(listener net.Listener) {
	s.listener = listener
	s.plugins = collector.New[string, *processor]()
	s.async = make(chan *AsyncData, 10)

	go s.serve()
}

// serve listens for incoming connections and handles them in separate goroutines.
func (s *Socket) serve() {
	for {
		conn, err := s.listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			continue
		} else if err != nil {
			slog.Error("accept connection", "error", err)
			return
		}

		if s.shutdown.Load() {
			return
		}

		s.waiter.Add(1)
		go s.handleConnection(newCodec(conn))
	}
}

// handleConnection manages the connection lifecycle for a plugin.
func (s *Socket) handleConnection(c *codec) {
	defer c.close()
	defer s.waiter.Done()

	// Handle handshake to read plugin information
	var info PluginInfo
	if err := c.read(&info); err != nil {
		slog.Error("handshake", "error", err)
		return
	}

	plug := &processor{PluginInfo: info, codec: c, pending: make(map[uint64]*call), end: make(chan struct{})}

	s.plugins.Store(info.Name, plug)
	defer s.plugins.Delete(info.Name)

	if !s.shutdown.Load() {
		plug.receive(s.async) // Start receiving messages for this plugin
	}
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

// Connected returns a list of connected plugins.
func (s *Socket) Connected() []PluginInfo {
	connected := make([]PluginInfo, 0)

	s.plugins.Range(func(_ string, p *processor) bool {
		connected = append(connected, p.PluginInfo)
		return true
	})

	return connected
}

// Unplug sends a stop request to a plugin based on its name.
// An optional 'id' can be provided to trace the request.
func (s *Socket) Unplug(id string, name string) {
	if plug, ok := s.plugins.Load(name); ok && !plug.shutdown.Load() {
		plug.stop(id)
	}
}

// Shutdown gracefully stops all running plugins and stops the Socket.
// An optional 'id' can be provided to trace the request.
func (s *Socket) Shutdown(id string) error {
	if s.shutdown.Swap(true) {
		return nil
	}

	close(s.async)

	s.plugins.Range(func(_ string, p *processor) bool {
		if !p.shutdown.Load() {
			go p.stop(id)
		}
		return true
	})

	s.waiter.Wait() // Wait until all plugins are stopped

	return s.listener.Close()
}

type call struct {
	response chan any
	error    error
}

// processor implements the socket-side plugin processor.
type processor struct {
	PluginInfo

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

func (p *processor) receive(async chan *AsyncData) {
	defer close(p.end)

	for {
		e := new(Envelope)
		if err := p.codec.read(e); err != nil {
			return
		}
		go p.post(async, e)
	}
}

func (p *processor) post(async chan *AsyncData, e *Envelope) {
	if e.Method == MethodAsync {
		a := &AsyncData{Name: p.Name}
		if err := e.decode(&a.Payload); err != nil {
			slog.Error("async payload decode", "error", err)
			return
		}

		if !p.shutdown.Load() {
			async <- a
		}
		return
	}

	seq := e.Seq

	p.mu.Lock()
	c, ok := p.pending[seq]
	delete(p.pending, seq)
	p.mu.Unlock()

	if !ok {
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
	p.shutdown.Store(true)

	e := &Envelope{Trace: trace, Method: MethodShutdown}
	if err := p.codec.write(e); err != nil {
		slog.Error("shutdown plugin", "name", p.PluginInfo.Name, "version", p.PluginInfo.Version, "error", err)
	}

	<-p.end
}
