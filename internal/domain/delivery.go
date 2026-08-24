package domain

import "time"

// ChannelType identifies a delivery destination type.
type ChannelType string

const (
	ChannelEmail    ChannelType = "email"
	ChannelTelegram ChannelType = "telegram"
	ChannelWebhook  ChannelType = "webhook"
)

// DeliveryState is the lifecycle state of a single delivery attempt. It is kept
// separate from EventState on purpose.
type DeliveryState string

const (
	DeliveryPending    DeliveryState = "pending"
	DeliverySending    DeliveryState = "sending"
	DeliverySent       DeliveryState = "sent"
	DeliveryFailed     DeliveryState = "failed"
	DeliveryDeadLetter DeliveryState = "dead_letter"
)

// DeliveryKind is what a delivery attempt is announcing. The queue is
// self-describing on purpose: a channel cannot tell an alert from a recovery by
// looking at the event alone, because by the time a recovery is sent the event
// has already been marked resolved — and so has an alert that was still in
// flight when someone closed the incident.
type DeliveryKind string

const (
	// KindAlert announces that something happened. Every delivery created
	// before 0.4.0 is one, which is why it is the column default.
	KindAlert DeliveryKind = "alert"
	// KindRecovery announces that an incident is over. It is created when an
	// incident closes, and only for the channels that were told about it.
	KindRecovery DeliveryKind = "recovery"
)

// ValidDeliveryKind reports whether k is a known delivery kind.
func ValidDeliveryKind(k DeliveryKind) bool {
	switch k {
	case KindAlert, KindRecovery:
		return true
	default:
		return false
	}
}

// DeliveryAttempt is a single attempt to deliver an event through a channel.
// A row represents one channel instance's delivery job for one event; the
// attempt count increments as the worker retries, and terminal outcomes are
// `sent` or `dead_letter`.
type DeliveryAttempt struct {
	ID      string      `json:"id"`
	EventID string      `json:"event_id"`
	Channel ChannelType `json:"channel"`
	// ChannelName identifies the specific configured channel instance this
	// attempt targets. Multiple channels of the same type can be configured
	// (e.g. two Telegram chats), so the name disambiguates which one delivered.
	ChannelName string `json:"channel_name"`
	// Kind distinguishes the alert from the recovery notice for the same event.
	Kind        DeliveryKind  `json:"kind"`
	State       DeliveryState `json:"state"`
	Attempts    int           `json:"attempts"`
	MaxAttempts int           `json:"max_attempts"`
	NextRetryAt *time.Time    `json:"next_retry_at,omitempty"`
	LastError   string        `json:"last_error,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// ChannelTarget identifies one configured channel instance an event should be
// delivered to. In Community every event fans out to every configured target.
type ChannelTarget struct {
	Type ChannelType
	Name string
}

// DefaultMaxAttempts is the capped number of delivery tries before an attempt
// is moved to dead_letter.
const DefaultMaxAttempts = 5

// Notification is what a channel is asked to deliver: an event, and the reason
// it is being sent. Channels render an alert and a recovery differently, and
// the event alone does not carry that difference.
type Notification struct {
	Event *Event
	Kind  DeliveryKind
}

// Alert builds a Notification announcing an event.
func Alert(e *Event) Notification { return Notification{Event: e, Kind: KindAlert} }

// Recovery builds a Notification announcing that an incident is over.
func Recovery(e *Event) Notification { return Notification{Event: e, Kind: KindRecovery} }

// IsRecovery reports whether n announces the end of an incident.
func (n Notification) IsRecovery() bool { return n.Kind == KindRecovery }
