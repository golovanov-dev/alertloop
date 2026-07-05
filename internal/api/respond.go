// Package api implements AlertLoop's HTTP layer: the versioned JSON API, auth
// middleware, health/readiness probes, the events web page, and OpenAPI/Swagger
// serving.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// errorBody is the standard JSON error envelope.
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// listResponse is the standard cursor-paginated list envelope.
type listResponse[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// orEmpty returns a non-nil slice so an empty result serializes as a JSON `[]`
// rather than `null` (which breaks strict clients that expect an array).
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			slog.Error("failed to encode response", "error", err)
		}
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(w, status, body)
}

// writeDomainError maps a domain error to an appropriate HTTP status.
func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, domain.ErrValidation):
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
	case errors.Is(err, domain.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "invalid_transition", err.Error())
	case errors.Is(err, domain.ErrInvalidAction):
		writeError(w, http.StatusBadRequest, "invalid_action", err.Error())
	case errors.Is(err, domain.ErrNotReplayable):
		writeError(w, http.StatusConflict, "not_replayable", err.Error())
	default:
		slog.Error("internal error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
