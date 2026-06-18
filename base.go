package brpc

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/easysy/brpc/collector"
)

type pluginCaller interface {
	call(trace, method string, payload any) (any, error)
	DeepCopy(full bool) *PluginInfo
}

type result struct {
	name     string
	response any
	err      error
}

type base[T pluginCaller] struct {
	plugins  collector.Collector[string, T]
	async    chan *AsyncData
	shutdown atomic.Bool
	wg       sync.WaitGroup
}

func (b *base[T]) Call(id, name, method string, payload any) (any, error) {
	if b.shutdown.Load() {
		return nil, ErrShutdown
	}
	if plug, ok := b.plugins.Load(name); ok {
		return plug.call(id, method, payload)
	}
	return nil, errors.New("plugin " + name + " not found")
}

func (b *base[T]) Broadcast(id, method string, payload any) (map[string]any, map[string]error) {
	if b.shutdown.Load() {
		return map[string]any{}, map[string]error{}
	}

	var wg sync.WaitGroup
	res := make(chan result)
	rs, es := make(map[string]any), make(map[string]error)

	b.plugins.Range(func(k string, p T) bool {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := p.call(id, method, payload)
			res <- result{name: k, response: r, err: e}
		}()
		return true
	})

	var cwg sync.WaitGroup
	cwg.Add(1)
	go func() {
		defer cwg.Done()
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
	cwg.Wait()

	return rs, es
}

func (b *base[T]) Async() (*AsyncData, error) {
	if c := <-b.async; c != nil {
		return c, nil
	}
	return nil, ErrShutdown
}

func (b *base[T]) Registered(full bool) map[string]*PluginInfo {
	registered := make(map[string]*PluginInfo)
	b.plugins.Range(func(name string, p T) bool {
		registered[name] = p.DeepCopy(full)
		return true
	})
	return registered
}

func (b *base[T]) PluginInfo(name string) *PluginInfo {
	if p, ok := b.plugins.Load(name); ok {
		return p.DeepCopy(true)
	}
	return nil
}

// sendAsync drops the message if no reader consumes it within one second.
func (b *base[T]) sendAsync(a *AsyncData) {
	defer b.wg.Done()
	select {
	case b.async <- a:
	case <-time.NewTimer(time.Second).C:
	}
}
