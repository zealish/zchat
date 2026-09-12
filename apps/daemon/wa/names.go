package wa

import (
	"sync"

	"go.mau.fi/whatsmeow/types"
)

// nameCache memoises contact-name and phone-number lookups.
//
// Resolving one sender costs a LID-map lookup plus a contacts lookup, and both
// run against the daemon's single SQLite writer. A 200-message page repeats
// that for every distinct sender whose row was stored before the name was
// known, which is the bulk of a history-synced database. Caching turns the
// per-page cost from hundreds of queries into a handful.
//
// A miss is cached as an empty string, which is just as worth remembering as a
// hit: an unknown contact stays unknown until an event says otherwise.
type nameCache struct {
	mu      sync.RWMutex
	names   map[string]string
	numbers map[string]string
}

func newNameCache() *nameCache {
	return &nameCache{names: make(map[string]string), numbers: make(map[string]string)}
}

func (c *nameCache) name(jid string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.names[jid]
	return v, ok
}

func (c *nameCache) putName(jid, name string) {
	c.mu.Lock()
	c.names[jid] = name
	c.mu.Unlock()
}

func (c *nameCache) number(jid string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.numbers[jid]
	return v, ok
}

func (c *nameCache) putNumber(jid, number string) {
	c.mu.Lock()
	c.numbers[jid] = number
	c.mu.Unlock()
}

// drop forgets a JID whose name may have changed. The LID/phone mapping is
// stable, so only the name side is invalidated.
func (c *nameCache) drop(jid types.JID) {
	c.mu.Lock()
	delete(c.names, jid.ToNonAD().String())
	c.mu.Unlock()
}

// reset clears everything, which a re-pairing invalidates.
func (c *nameCache) reset() {
	c.mu.Lock()
	c.names = make(map[string]string)
	c.numbers = make(map[string]string)
	c.mu.Unlock()
}
