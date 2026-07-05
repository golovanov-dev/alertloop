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

// ValidSeverity reports whether s is a known severity.
func ValidSeverity(s Severity) bool {
	switch s {
	case SeverityInfo, SeveritySuccess, SeverityWarning, SeverityError, SeverityCritical:
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
// state, or an error if the transition is not allowed. The state machine is
// deliberately permissive but rejects transitions out of terminal/blank states
// that would be meaningless.
func ApplyAction(current EventState, action EventAction) (EventState, error) {
	switch action {
	case ActionAck:
		if current == StateResolved {
			return "", fmt.Errorf("%w: cannot acknowledge a resolved event", ErrInvalidTransition)
		}
		return StateAcknowledged, nil
	case ActionResolve:
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
