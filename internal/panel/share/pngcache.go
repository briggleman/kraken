package share

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// A chat service fetches a card image two or three times in quick succession
// (its unfurler, then its image proxy, then retries), and a server's card
// only changes when what it shows changes. PNGCache keeps recent renders so a
// burst costs one render, keyed by everything the image shows.

const (
	pngTTL       = 2 * time.Minute
	pngMaxCached = 64
)

type pngEntry struct {
	data []byte
	at   time.Time
}

// PNGCache holds recently rendered card images.
type PNGCache struct {
	mu    sync.Mutex
	items map[string]pngEntry
}

// NewPNGCache returns an empty cache.
func NewPNGCache() *PNGCache { return &PNGCache{items: map[string]pngEntry{}} }

// Key is the cache key for a card: every field the image draws, with the
// as-of time to the minute (the stamp shows minutes). It is a plain string,
// not a digest: it only ever lives in this process's map, beside the PNG that
// already shows the same values, so hashing it would protect nothing.
func (c *Card) Key() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q|%q|%q|%q|%d|%d|%t|%s", c.ServerName, c.Game, c.BannerURL, c.State,
		c.Players, c.MaxPlayers, c.PlayersKnown, c.AsOf.UTC().Format("2006-01-02T15:04"))
	for _, j := range c.Join {
		fmt.Fprintf(&b, "|%s=%q", j.Kind, j.Value)
	}
	return b.String()
}

// Get returns a fresh render for key, if there is one.
func (p *PNGCache) Get(key string, now time.Time) ([]byte, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.items[key]
	if !ok || now.Sub(e.at) >= pngTTL {
		return nil, false
	}
	return e.data, true
}

// Put stores a render, dropping expired entries and then the oldest when full.
func (p *PNGCache) Put(key string, data []byte, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.items {
		if now.Sub(e.at) >= pngTTL {
			delete(p.items, k)
		}
	}
	if len(p.items) >= pngMaxCached {
		var oldest string
		var at time.Time
		for k, e := range p.items {
			if oldest == "" || e.at.Before(at) {
				oldest, at = k, e.at
			}
		}
		delete(p.items, oldest)
	}
	p.items[key] = pngEntry{data: data, at: now}
}
