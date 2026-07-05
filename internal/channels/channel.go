// Package channels defines the delivery channel interface and the Email,
// Telegram, and Webhook implementations shipped with Community Edition. Every
// channel has explicit timeout, failure, and (for webhooks) signing behavior;
// transport errors are returned so the delivery worker can record and retry
// them.
//
// Multiple channels of the same type may be configured (e.g. two Telegram
// chats); each carries a unique Name so deliveries and history can distinguish
// them. Community fans every event out to all configured channels; per-event
// routing rules are a paid capability.
package channels

import (
	"context"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// Channel delivers a single event notification to one configured destination.
type Channel interface {
	// Type reports the channel type this implementation handles.
	Type() domain.ChannelType
	// Name is the unique identifier of this configured channel instance.
	Name() string
	// Send delivers the event. A non-nil error means the attempt failed and is
	// eligible for retry. Send must respect ctx cancellation/deadlines.
	Send(ctx context.Context, e *domain.Event) error
}

// Registry holds the configured channels, keyed by their unique name. In
// Community every event fans out to every registered channel.
type Registry struct {
	byName map[string]Channel
	order  []string
}

// NewRegistry builds a Registry from the given channels, ignoring nils. If two
// channels share a name the later one wins (config validation should prevent
// this).
func NewRegistry(chans ...Channel) *Registry {
	r := &Registry{byName: map[string]Channel{}}
	for _, c := range chans {
		if c == nil {
			continue
		}
		if _, ok := r.byName[c.Name()]; !ok {
			r.order = append(r.order, c.Name())
		}
		r.byName[c.Name()] = c
	}
	return r
}

// Get returns the channel with the given name, if registered.
func (r *Registry) Get(name string) (Channel, bool) {
	c, ok := r.byName[name]
	return c, ok
}

// Targets lists every registered channel as a delivery target, in registration
// order. Used by ingestion to fan out delivery jobs.
func (r *Registry) Targets() []domain.ChannelTarget {
	out := make([]domain.ChannelTarget, 0, len(r.order))
	for _, name := range r.order {
		c := r.byName[name]
		out = append(out, domain.ChannelTarget{Type: c.Type(), Name: c.Name()})
	}
	return out
}

// Len reports how many channels are registered.
func (r *Registry) Len() int { return len(r.byName) }
