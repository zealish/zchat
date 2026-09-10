package service

import (
	"sync"

	"github.com/rs/zerolog"

	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

const subscriberBuffer = 256

// Broker fans events out to every connected desktop client.
type Broker struct {
	log zerolog.Logger

	mu   sync.Mutex
	subs map[int]chan *zchatv1.Event
	next int
}

// NewBroker builds an empty broker.
func NewBroker(log zerolog.Logger) *Broker {
	return &Broker{log: log, subs: make(map[int]chan *zchatv1.Event)}
}

// Subscribe registers a new listener and returns it with its unsubscribe func.
func (b *Broker) Subscribe() (<-chan *zchatv1.Event, func()) {
	ch := make(chan *zchatv1.Event, subscriberBuffer)

	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		if existing, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(existing)
		}
		b.mu.Unlock()
	}
}

// Publish delivers an event to all subscribers, dropping it for any listener
// that has fallen behind. A stalled UI must never block the whatsmeow loop.
func (b *Broker) Publish(evt *zchatv1.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for id, ch := range b.subs {
		select {
		case ch <- evt:
		default:
			b.log.Warn().Int("subscriber", id).Msg("event dropped, subscriber too slow")
		}
	}
}
