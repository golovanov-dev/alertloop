package domain

import (
	"errors"
	"testing"
)

func TestApplyAction(t *testing.T) {
	tests := []struct {
		name    string
		current EventState
		action  EventAction
		want    EventState
		wantErr error
	}{
		{"ack new", StateNew, ActionAck, StateAcknowledged, nil},
		{"resolve acknowledged", StateAcknowledged, ActionResolve, StateResolved, nil},
		{"escalate new", StateNew, ActionEscalate, StateEscalated, nil},
		{"mute new", StateNew, ActionMute, StateMuted, nil},
		{"unmute muted", StateMuted, ActionUnmute, StateNew, nil},
		{"ack resolved rejected", StateResolved, ActionAck, "", ErrInvalidTransition},
		{"mute resolved rejected", StateResolved, ActionMute, "", ErrInvalidTransition},
		{"escalate resolved rejected", StateResolved, ActionEscalate, "", ErrInvalidTransition},
		{"unmute non-muted rejected", StateNew, ActionUnmute, "", ErrInvalidTransition},
		{"resolve from any state", StateEscalated, ActionResolve, StateResolved, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ApplyAction(tt.current, tt.action)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got state %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseAction(t *testing.T) {
	for _, s := range []string{"ack", "resolve", "mute", "unmute", "escalate"} {
		if _, ok := ParseAction(s); !ok {
			t.Errorf("expected %q to parse", s)
		}
	}
	if _, ok := ParseAction("delete"); ok {
		t.Error("expected unknown action to fail parsing")
	}
}

func TestValidators(t *testing.T) {
	if !ValidEventType(EventIncident) || ValidEventType("nope") {
		t.Error("ValidEventType wrong")
	}
	if !ValidSeverity(SeverityCritical) || ValidSeverity("nope") {
		t.Error("ValidSeverity wrong")
	}
	if !ValidEventState(StateMuted) || ValidEventState("nope") {
		t.Error("ValidEventState wrong")
	}
}
