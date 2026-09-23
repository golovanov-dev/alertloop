package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventType is the family an event belongs to. Families are kept distinct so
// that technical incidents, business notifications, and audit trail entries are
// never conflated.
type EventType string

const (
	// EventIncident is a technical or operational failure.
	EventIncident EventType = "incident"
	// EventBusiness is a domain event a manager/admin may care about.
	EventBusiness EventType = "business_event"
	// EventAudit is a security/administrative trail entry.
	EventAudit EventType = "audit"
)

// ValidEventType reports whether t is a known event family.
func ValidEventType(t EventType) bool {
	switch t {
	case EventIncident, EventBusiness, EventAudit:
		return true
	default:
		return false
	}
}

// Severity classifies the importance of an event.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeveritySuccess  Severity = "success"
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

// severityRanks is the one list of severities, with how alarming each is.
// success shares info's rank: this is an order of alarm, not of importance,
// and a successful outcome is not more alarming than a notice.
var severityRanks = map[Severity]int{
	SeverityInfo:     10,
	SeveritySuccess:  10,
	SeverityWarning:  20,
	SeverityError:    30,
	SeverityCritical: 40,
}

// ValidSeverity reports whether s is a known severity.
func ValidSeverity(s Severity) bool {
	_, ok := severityRanks[s]
	return ok
}

// SeverityRank orders severities by how alarming they are; 0 for an unknown
// one.
func SeverityRank(s Severity) int {
	return severityRanks[s]
}

// IngestStatus is the lifecycle signal a monitoring source attaches to an
// incoming event. It is a property of the *request*, not of the stored event:
// `firing` asserts that a problem is happening now, `resolved` asserts that the
// problem identified by the same DedupeKey is over. The stored event's own
// lifecycle is EventState.
//
// Omitting it means `firing`, which is what every pre-0.4.0 client sends.
type IngestStatus string

const (
	// StatusFiring reports a problem as active. A repeated firing for an open
	// incident refreshes it rather than creating a second event.
	StatusFiring IngestStatus = "firing"
	// StatusResolved reports the problem as over and closes the open incident
	// carrying the same DedupeKey.
	StatusResolved IngestStatus = "resolved"
)

// ValidIngestStatus reports whether s is a known ingestion status.
func ValidIngestStatus(s IngestStatus) bool {
	switch s {
	case StatusFiring, StatusResolved:
		return true
	default:
		return false
	}
}

// EventState is the lifecycle state of an event. It is intentionally separate
// from delivery state: an event can be unresolved even when a channel delivery
// has succeeded, and a delivery can fail while the event is stored correctly.
type EventState string

const (
	StateNew          EventState = "new"
	StateAcknowledged EventState = "acknowledged"
	StateResolved     EventState = "resolved"
	StateMuted        EventState = "muted"
	StateEscalated    EventState = "escalated"
)

// ValidEventState reports whether s is a known event state.
func ValidEventState(s EventState) bool {
	switch s {
	case StateNew, StateAcknowledged, StateResolved, StateMuted, StateEscalated:
		return true
	default:
		return false
	}
}

// Event is a stored record that something important happened.
type Event struct {
	ID         string          `json:"id"`
	Type       EventType       `json:"type"`
	Severity   Severity        `json:"severity"`
	State      EventState      `json:"state"`
	Source     string          `json:"source"`
	Category   string          `json:"category,omitempty"`
	Message    string          `json:"message"`
	EntityType string          `json:"entity_type,omitempty"`
	EntityID   string          `json:"entity_id,omitempty"`
	TraceID    string          `json:"trace_id,omitempty"`
	DedupeKey  string          `json:"dedupe_key,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	// LastSeenAt is when this incident was last reported as still firing. For
	// an event ingested once it equals CreatedAt; a monitoring source that
	// repeats the same DedupeKey moves it forward without creating a new event.
	LastSeenAt time.Time `json:"last_seen_at"`
	// ResolvedAt is when the incident was closed, by an ingested
	// `status: resolved` or by the manual resolve action. Nil while open.
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// MaxPayloadBytes is the maximum accepted size of the event payload object.
const MaxPayloadBytes = 256 * 1024

// EventAction is a manual state transition requested through the API.
type EventAction string

const (
	ActionAck      EventAction = "ack"
	ActionResolve  EventAction = "resolve"
	ActionMute     EventAction = "mute"
	ActionUnmute   EventAction = "unmute"
	ActionEscalate EventAction = "escalate"
)

// ApplyAction returns the resulting state after applying action to the current
// state, or an error if the transition is not allowed. The server is the only
// place these rules live; the console offers what this function allows.
//
// `resolved` is terminal: nothing leaves it, and resolving it again is an error
// rather than a no-op, so a second resolve can never send a second recovery.
// Acknowledge and escalate are allowed from `muted`. Unmute always returns to
// `new`, whatever the state before mute was. Escalate is a label: it changes
// the state and sends nothing.
func ApplyAction(current EventState, action EventAction) (EventState, error) {
	switch action {
	case ActionAck:
		if current == StateResolved {
			return "", fmt.Errorf("%w: cannot acknowledge a resolved event", ErrInvalidTransition)
		}
		return StateAcknowledged, nil
	case ActionResolve:
		if current == StateResolved {
			return "", fmt.Errorf("%w: event is already resolved", ErrInvalidTransition)
		}
		return StateResolved, nil
	case ActionMute:
		if current == StateResolved {
			return "", fmt.Errorf("%w: cannot mute a resolved event", ErrInvalidTransition)
		}
		return StateMuted, nil
	case ActionUnmute:
		if current != StateMuted {
			return "", fmt.Errorf("%w: event is not muted", ErrInvalidTransition)
		}
		return StateNew, nil
	case ActionEscalate:
		if current == StateResolved {
			return "", fmt.Errorf("%w: cannot escalate a resolved event", ErrInvalidTransition)
		}
		return StateEscalated, nil
	default:
		return "", fmt.Errorf("%w: unknown action %q", ErrInvalidAction, action)
	}
}

// CheckActionAllowed reports whether manual actions apply to events of type t.
// Only incidents have a lifecycle; a business event or an audit entry records
// something that already happened and has nothing to acknowledge or close.
func CheckActionAllowed(t EventType) error {
	if t != EventIncident {
		return fmt.Errorf("%w: actions apply to incidents only, this event is %s", ErrInvalidTransition, t)
	}
	return nil
}

// ParseAction converts a URL action segment to an EventAction.
func ParseAction(s string) (EventAction, bool) {
	a := EventAction(s)
	switch a {
	case ActionAck, ActionResolve, ActionMute, ActionUnmute, ActionEscalate:
		return a, true
	default:
		return "", false
	}
}
