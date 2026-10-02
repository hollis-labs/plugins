package main

import (
	"sync"
	"time"
)

type oEmbedCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]oEmbedCacheEntry
}
type oEmbedCacheEntry struct {
	value     OEmbedResult
	expiresAt time.Time
}

func newOEmbedCache(ttl time.Duration) *oEmbedCache {
	return &oEmbedCache{ttl: ttl, entries: map[string]oEmbedCacheEntry{}}
}
func (c *oEmbedCache) get(key string) (*OEmbedResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if !time.Now().Before(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	value := entry.value
	return &value, true
}
func (c *oEmbedCache) set(key string, value *OEmbedResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, k)
		}
	}
	if len(c.entries) >= 512 {
		var oldest string
		var expires time.Time
		for k, entry := range c.entries {
			if oldest == "" || entry.expiresAt.Before(expires) {
				oldest = k
				expires = entry.expiresAt
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[key] = oEmbedCacheEntry{value: *value, expiresAt: now.Add(c.ttl)}
}
