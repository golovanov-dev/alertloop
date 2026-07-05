package domain

import "errors"

var (
	// ErrNotFound is returned when a requested entity does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalidTransition is returned when an event action is not allowed
	// from the current state.
	ErrInvalidTransition = errors.New("invalid state transition")
	// ErrInvalidAction is returned for an unknown event action.
	ErrInvalidAction = errors.New("invalid action")
	// ErrValidation is returned when incoming data fails validation.
	ErrValidation = errors.New("validation failed")
	// ErrNotReplayable is returned when a delivery attempt cannot be replayed
	// because it is not in the dead_letter state.
	ErrNotReplayable = errors.New("delivery attempt is not in dead_letter state")
)
