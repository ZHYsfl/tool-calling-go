// orchestration_bus.go implements OrchestrationBus, a lightweight in-process
// pub/sub event bus. It is the only piece that deals with subscribers and
// channel fan-out — no orchestration logic lives here.
package toolcalling

import (
	"sync"
	"time"
)

// OrchestrationBus is a run-scoped pub/sub bus for OrchestrationEvent.
// Subscribe with a specific runID or "*" to receive events from all runs.
type OrchestrationBus struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan OrchestrationEvent]struct{}
}

func newOrchestrationBus() *OrchestrationBus {
	return &OrchestrationBus{
		subscribers: make(map[string]map[chan OrchestrationEvent]struct{}),
	}
}

// Subscribe returns a read-only channel that receives events for the given
// runID (or "*" for all runs), and an unsubscribe function that closes the
// channel and removes the subscription.
func (b *OrchestrationBus) Subscribe(runID string, buffer int) (<-chan OrchestrationEvent, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan OrchestrationEvent, buffer)

	b.mu.Lock()
	if b.subscribers[runID] == nil {
		b.subscribers[runID] = make(map[chan OrchestrationEvent]struct{})
	}
	b.subscribers[runID][ch] = struct{}{}
	b.mu.Unlock()

	unsub := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if subs, ok := b.subscribers[runID]; ok {
			if _, exists := subs[ch]; exists {
				delete(subs, ch)
				close(ch)
			}
			if len(subs) == 0 {
				delete(b.subscribers, runID)
			}
		}
	}
	return ch, unsub
}

// Publish fans out an event to all subscribers matching event.RunID and the
// wildcard "*". Slow consumers are dropped (non-blocking send).
func (b *OrchestrationBus) Publish(event OrchestrationEvent) {
	if event.At.IsZero() {
		event.At = time.Now()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch := range b.subscribers[event.RunID] {
		select {
		case ch <- event:
		default:
		}
	}
	for ch := range b.subscribers["*"] {
		select {
		case ch <- event:
		default:
		}
	}
}
