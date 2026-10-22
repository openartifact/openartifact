// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"container/list"
	"sync"
)

const (
	// depCacheEntries bounds how many packages' dependency groups are kept.
	depCacheEntries = 4096
	// maxConcurrentNuspecReads bounds the nupkg blobs read (and held in memory)
	// at once to compute dependency groups on a cache miss.
	maxConcurrentNuspecReads = 2
)

// depCache is a bounded LRU of dependency groups keyed by package blob
// digest. Blobs are content-addressed, so an entry never goes stale.
type depCache struct {
	mu    sync.Mutex
	max   int
	order *list.List // front = most recently used
	items map[string]*list.Element
}

type depCacheEntry struct {
	key    string
	groups []map[string]any
}

func newDepCache(maxEntries int) *depCache {
	return &depCache{max: max(maxEntries, 1), order: list.New(), items: make(map[string]*list.Element)}
}

func (c *depCache) get(key string) ([]map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return nil, false
	}

	c.order.MoveToFront(el)

	entry, _ := el.Value.(*depCacheEntry)

	return entry.groups, true
}

func (c *depCache) put(key string, groups []map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)

		if entry, _ := el.Value.(*depCacheEntry); entry != nil {
			entry.groups = groups
		}

		return
	}

	c.items[key] = c.order.PushFront(&depCacheEntry{key: key, groups: groups})

	for c.order.Len() > c.max {
		oldest := c.order.Back()
		c.order.Remove(oldest)

		if entry, _ := oldest.Value.(*depCacheEntry); entry != nil {
			delete(c.items, entry.key)
		}
	}
}
