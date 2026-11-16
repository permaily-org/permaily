package cimd

import (
	"container/list"
	"time"

	"github.com/permaily-org/permaily/internal/vendors"
)

// entry is a document or a key set as fetched and checked, with what its
// answer said of caching it and when it was last fetched or revalidated.
type entry struct {
	key       string
	value     any // *oauth.ClientDocument or []vendors.Key
	policy    vendors.CachePolicy
	validated time.Time
	etag      string
	modified  string
}

// lru holds at most max entries, the least recently used dropped first.
type lru struct {
	max   int
	order *list.List // of *entry, the most recently used first
	byKey map[string]*list.Element
}

func newLRU(max int) *lru {
	return &lru{max: max, order: list.New(), byKey: map[string]*list.Element{}}
}

// get is the entry of key, now the most recently used; nil when not held.
func (c *lru) get(key string) *entry {
	el, ok := c.byKey[key]
	if !ok {
		return nil
	}
	c.order.MoveToFront(el)
	return el.Value.(*entry)
}

// put keeps e under its key, in place of what was there.
func (c *lru) put(e *entry) {
	if el, ok := c.byKey[e.key]; ok {
		el.Value = e
		c.order.MoveToFront(el)
		return
	}
	c.byKey[e.key] = c.order.PushFront(e)
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.byKey, last.Value.(*entry).key)
	}
}

// remove drops key.
func (c *lru) remove(key string) {
	if el, ok := c.byKey[key]; ok {
		c.order.Remove(el)
		delete(c.byKey, key)
	}
}

// len is how many entries are held.
func (c *lru) len() int { return c.order.Len() }
