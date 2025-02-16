package collector

import (
	"sync"
)

// Collector is a generic interface that allows storing, listing, loading, and deleting key-value pairs.
// K is the type of the key, and V is the type of the value.
type Collector[K comparable, V any] interface {
	Store(key K, val V)                      // Store saves the value associated with the given key in the map
	StoreIfExists(key K, val V, f func(K) K) // StoreIfExists saves the value associated with the given key in the map with a key func
	Load(key K) (V, bool)                    // Load returns the value stored in the map for a key and a boolean indicating whether the key present
	Delete(key K)                            // Delete deletes the value for a key
	LoadAndDelete(key K) (V, bool)           // LoadAndDelete deletes the value for a key, returns the previous value if any and a boolean indicating whether the key present
	Range(f func(K, V) bool)                 // Range calls f sequentially for each key and value present in the map
}

// New returns a new instance of a Collector, initialized with an empty map.
// The map uses K as the key type and V as the value type.
func New[K comparable, V any]() Collector[K, V] { return &collector[K, V]{mp: make(map[K]V)} }

// collector is a concrete implementation of the Collector interface.
// It uses a map to store the key-value pairs, with synchronization for concurrent access.
type collector[K comparable, V any] struct {
	mu sync.RWMutex
	mp map[K]V
}

// Store saves the value associated with the given key in the map.
// It locks the map for writing.
func (c *collector[K, V]) Store(key K, val V) {
	c.mu.Lock()
	c.mp[key] = val
	c.mu.Unlock()
}

func (c *collector[K, V]) exists(key K) bool {
	_, ok := c.mp[key]
	return ok
}

// StoreIfExists saves the value associated with the given key in the map.
//
// It checks if the key exists in the map, if it does, it continuously calls `f` to modify the key until a unique one is found.
//
// It will panic `f` is nil. It locks the map for writing.
func (c *collector[K, V]) StoreIfExists(key K, val V, f func(K) K) {
	c.mu.Lock()
	for c.exists(key) {
		key = f(key)
	}
	c.mp[key] = val
	c.mu.Unlock()
}

// Load returns the value stored in the map for a key and a boolean indicating whether the key present.
// It locks the map for reading.
func (c *collector[K, V]) Load(key K) (V, bool) {
	c.mu.RLock()
	val, ok := c.mp[key]
	c.mu.RUnlock()
	return val, ok
}

// Delete deletes the value for a key.
// It locks the map for writing.
func (c *collector[K, V]) Delete(key K) {
	c.mu.Lock()
	delete(c.mp, key)
	c.mu.Unlock()
}

// LoadAndDelete deletes the value for a key, returns the previous value if any and a boolean indicating whether the key present.
// It locks the map for writing.
func (c *collector[K, V]) LoadAndDelete(key K) (V, bool) {
	c.mu.Lock()
	val, ok := c.mp[key]
	delete(c.mp, key)
	c.mu.Unlock()
	return val, ok
}

// Range calls f sequentially for each key and value present in the map.
// It locks the map for reading.
// If f returns false, range stops the iteration.
func (c *collector[K, V]) Range(f func(K, V) bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for k, v := range c.mp {
		if !f(k, v) {
			break
		}
	}
}
