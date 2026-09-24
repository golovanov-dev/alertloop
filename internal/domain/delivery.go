package domain

import "time"

// ChannelType identifies a delivery destination type.
type ChannelType string

const (
	ChannelEmail    ChannelType = "email"
	ChannelTelegram ChannelType = "telegram"
	ChannelWebhook  ChannelType = "webhook"
	// ChannelSlack is a Slack incoming webhook; Mattermost and Rocket.Chat
	// accept the same message format.
	ChannelSlack    ChannelType = "slack"
	ChannelTeams    ChannelType = "teams"
	ChannelDiscord  ChannelType = "discord"
	ChannelNtfy     ChannelType = "ntfy"
	ChannelPushover ChannelType = "pushover"
)

// ValidChannelType reports whether t is a known channel type.
func ValidChannelType(t ChannelType) bool {
	switch t {
	case ChannelEmail, ChannelTelegram, ChannelWebhook,
		ChannelSlack, ChannelTeams, ChannelDiscord, ChannelNtfy, ChannelPushover:
		return true
	default:
		return false
	}
}

// DeliveryState is the lifecycle state of a single delivery attempt. It is kept
// separate from EventState on purpose.
type DeliveryState string

const (
	DeliveryPending    DeliveryState = "pending"
	DeliverySending    DeliveryState = "sending"
	DeliverySent       DeliveryState = "sent"
	DeliveryFailed     DeliveryState = "failed"
	DeliveryDeadLetter DeliveryState = "dead_letter"
	// DeliveryCancelled is an alert that was still waiting to be sent when its
	// incident was muted. It is final: the worker does not take it, replay does
	// not apply to it, and unmute does not queue it again.
	DeliveryCancelled DeliveryState = "cancelled"
)

// ValidDeliveryState reports whether s is a known delivery state.
func ValidDeliveryState(s DeliveryState) bool {
	switch s {
	case DeliveryPending, DeliverySending, DeliverySent, DeliveryFailed, DeliveryDeadLetter, DeliveryCancelled:
		return true
	default:
		return false
	}
}

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

// OrAlert is k, or KindAlert when k is empty: a delivery that does not say what
// it announces is an alert, as every delivery was before recovery notices.
func (k DeliveryKind) OrAlert() DeliveryKind {
	if k == "" {
		return KindAlert
	}
	return k
}

// DefaultChannelTimeout bounds one send to a channel whose configuration sets
// no timeout of its own.
const DefaultChannelTimeout = 10 * time.Second

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
// `sent`, `dead_letter`, or `cancelled`.
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
	// FallbackOf is the dead-lettered alert this attempt redirects to a
	// channel's fallback; nil for every other attempt.
	FallbackOf *AttemptLink `json:"fallback_of,omitempty"`
	// FallbackTo is the attempt this dead-lettered alert was redirected to.
	// Filled when attempts are read for the API; nil otherwise.
	FallbackTo *AttemptLink `json:"fallback_to,omitempty"`
	// RecoveryFor is the alert attempt a recovery follows: the recovery is
	// not sent until that alert is. Nil on alerts, and on a recovery written
	// before 0.8.0 whose channel had no alert of the event on record.
	RecoveryFor *AttemptLink `json:"recovery_for,omitempty"`
}

// AttemptLink names another delivery attempt of the same event.
type AttemptLink struct {
	ID          string `json:"id"`
	ChannelName string `json:"channel_name,omitempty"`
	// State is the linked attempt's state: whether an alert redirected to a
	// fallback got through is what tells a lost notification from a delivered
	// one.
	State DeliveryState `json:"state,omitempty"`
}

// IsFallback reports whether d redirects another channel's dead-lettered alert.
func (d *DeliveryAttempt) IsFallback() bool { return d.FallbackOf != nil }

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
	// Fallback is set when this alert is redirected from a channel that could
	// not deliver it.
	Fallback *FallbackOrigin
	// EventURL is the event's page in the admin console, built from
	// public_url; empty when public_url is not configured.
	EventURL string
}

// FallbackOrigin is the channel a redirected alert failed on, and its last
// error as stored (channels redact their secrets before storing it).
type FallbackOrigin struct {
	Channel string `json:"channel"`
	Error   string `json:"error,omitempty"`
	// Attempt is the id of the dead-lettered attempt. Not in the webhook
	// payload; it makes the redirected email's Message-ID its own.
	Attempt string `json:"-"`
}

// Alert builds a Notification announcing an event.
func Alert(e *Event) Notification { return Notification{Event: e, Kind: KindAlert} }

// Recovery builds a Notification announcing that an incident is over.
func Recovery(e *Event) Notification { return Notification{Event: e, Kind: KindRecovery} }

// IsRecovery reports whether n announces the end of an incident.
func (n Notification) IsRecovery() bool { return n.Kind == KindRecovery }
