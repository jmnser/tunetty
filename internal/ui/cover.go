package ui

import (
	"container/list"
	"context"
	"sync"

	"github.com/jmnser/tunetty/internal/art"
	"github.com/jmnser/tunetty/internal/subsonic"
)

// coverCache keeps decoded covers in memory, evicting least recently used.
// Decoding and rescaling a cover costs far more than the few hundred kilobytes
// an entry occupies, and users cycle through the same albums repeatedly.
type coverCache struct {
	mu    sync.Mutex
	limit int
	order *list.List               // most recent at the front
	items map[string]*list.Element // cover art id -> element
	fails map[string]struct{}      // ids known to have no artwork
}

type coverEntry struct {
	id  string
	img *art.Image
}

func newCoverCache(limit int) *coverCache {
	if limit < 1 {
		limit = 16
	}
	return &coverCache{
		limit: limit,
		order: list.New(),
		items: map[string]*list.Element{},
		fails: map[string]struct{}{},
	}
}

func (c *coverCache) get(id string) (*art.Image, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[id]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*coverEntry).img, true
}

// missing reports whether a previous fetch established that this id has no
// artwork, so the UI does not retry on every track change.
func (c *coverCache) missing(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.fails[id]
	return ok
}

func (c *coverCache) markMissing(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fails[id] = struct{}{}
}

func (c *coverCache) put(id string, img *art.Image) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[id]; ok {
		el.Value.(*coverEntry).img = img
		c.order.MoveToFront(el)
		return
	}
	c.items[id] = c.order.PushFront(&coverEntry{id: id, img: img})
	for c.order.Len() > c.limit {
		back := c.order.Back()
		if back == nil {
			break
		}
		c.order.Remove(back)
		delete(c.items, back.Value.(*coverEntry).id)
	}
}

// fetchCover downloads and decodes cover art at a pixel size suited to the
// display area, so the server does the downscaling instead of the client.
func fetchCover(ctx context.Context, cl *subsonic.Client, id string, px int) (*art.Image, error) {
	b, err := cl.CoverArt(ctx, id, px)
	if err != nil {
		return nil, err
	}
	return art.Decode(b)
}
