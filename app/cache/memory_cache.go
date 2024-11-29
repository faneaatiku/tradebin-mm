package cache

import (
	"sync"
	"time"
)

var cacheService *InMemoryCache
var once sync.Once

// cacheEntry holds the data and a timer for the expiration
type cacheEntry struct {
	data  []byte
	timer *time.Timer
}

// InMemoryCache is a thread-safe in-memory cache
type InMemoryCache struct {
	mu    sync.RWMutex
	cache map[string]*cacheEntry
}

func GetMemoryCache() *InMemoryCache {
	once.Do(func() {
		cacheService = NewInMemoryCache()
	})

	return cacheService
}

// NewInMemoryCache creates a new instance of InMemoryCache
func NewInMemoryCache() *InMemoryCache {
	return &InMemoryCache{
		cache: make(map[string]*cacheEntry),
	}
}

// Get retrieves data from the cache by key
func (c *InMemoryCache) Get(key string) ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, exists := c.cache[key]
	if !exists {
		return nil, nil
	}
	return entry.data, nil
}

// Set stores data in the cache with the given key and expiration
func (c *InMemoryCache) Set(key string, data []byte, expiration time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If the key already exists, stop the previous timer
	if entry, exists := c.cache[key]; exists {
		entry.timer.Stop()
	}

	// Create a timer to delete the key after expiration
	timer := time.AfterFunc(expiration, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.cache, key)
	})

	// Store the data and the timer in the cache
	c.cache[key] = &cacheEntry{
		data:  data,
		timer: timer,
	}

	return nil
}
