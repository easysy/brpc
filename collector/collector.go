package collector

import (
	"sync"
)

// Collector is a generic interface that allows storing, listing, loading, and deleting key-value pairs.
// K is the type of the key, and V is the type of the value.
type Collector[K comparable, V any] interface {
	Store(key K, val V)                                                 // Store saves the value associated with the given key in the map
	StoreWithKeyResolver(key K, val V, f func(K) K, attempts uint) bool // StoreWithKeyResolver attempts to store a key-value pair in the map
	Load(key K) (V, bool)                                               // Load returns the value stored in the map for a key and a boolean indicating whether the key present
	Delete(key K)                                                       // Delete deletes the value for a key
	LoadAndDelete(key K) (V, bool)                                      // LoadAndDelete deletes the value for a key, returns the previous value if any and a boolean indicating whether the key present
	Range(f func(K, V) bool)                                            // Range calls f sequentially for each key and value present in the map
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

// StoreWithKeyResolver attempts to store a key-value pair in the map.
//
//   - If the key does not exist, it stores the value immediately and returns true.
//   - If the key exists and `f` is provided, it modifies the key using `f`
//     until a unique key is found (up to `attempts` times).
//   - If `f` is nil or the attempt limit is reached, it returns false without storing the value.
//
// It locks the map for writing.
func (c *collector[K, V]) StoreWithKeyResolver(key K, val V, f func(K) K, attempts uint) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If the key does not exist, store it immediately
	if !c.exists(key) {
		c.mp[key] = val
		return true
	}

	// If no resolver function is provided or attempts are zero, fail
	if f == nil || attempts == 0 {
		return false
	}

	// Attempt to resolve key conflict by modifying the key
	for attempts > 0 {
		key = f(key)
		attempts--

		// If key becomes unique, store the value and return true
		if !c.exists(key) {
			c.mp[key] = val
			return true
		}
	}

	// If we run out of attempts without finding a unique key, return false
	return false
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
			return
		}
	}
}

func (c *collector[K, V]) exists(key K) bool {
	_, ok := c.mp[key]
	return ok
}
