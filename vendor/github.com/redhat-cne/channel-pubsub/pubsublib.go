// Based on the work at:
// https://eli.thegreenplace.net/2020/pubsub-using-channels-in-go/
//
// PubSub implementation where Subscribe returns a channel.
// Eli Bendersky [https://eli.thegreenplace.net]

package channelpubsub

import (
	"sync"

	exports "github.com/redhat-cne/ptp-listener-exports"
)

type Pubsub struct {
	mu     sync.RWMutex
	subs   map[string][]chan exports.StoredEvent
	closed bool
}

func NewPubsub() *Pubsub {
	ps := &Pubsub{}
	ps.subs = make(map[string][]chan exports.StoredEvent)
	ps.closed = false
	return ps
}

func (ps *Pubsub) Subscribe(topic string) <-chan exports.StoredEvent {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	ch := make(chan exports.StoredEvent, 1)
	ps.subs[topic] = append(ps.subs[topic], ch)
	return ch
}

func (ps *Pubsub) Publish(topic string, msg exports.StoredEvent) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	if ps.closed {
		return
	}

	for _, ch := range ps.subs[topic] {
		ch <- msg
	}
}

func (ps *Pubsub) Close() {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if !ps.closed {
		ps.closed = true
		for _, subs := range ps.subs {
			for _, ch := range subs {
				close(ch)
			}
		}
	}
}
