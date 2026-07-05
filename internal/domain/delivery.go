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
	ChannelName string        `json:"channel_name"`
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
