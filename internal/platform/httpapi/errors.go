package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const maxBody = 1 << 20

type APIError struct {
	Status  int            `json:"-"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func Err(status int, code, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

func ErrWithDetails(status int, code, message string, details map[string]any) *APIError {
	return &APIError{Status: status, Code: code, Message: message, Details: details}
}

var (
	ErrUnauthorized = Err(http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required")
	ErrInvalidToken = Err(http.StatusUnauthorized, "INVALID_TOKEN", "invalid token")
	ErrTokenExpired = Err(http.StatusUnauthorized, "TOKEN_EXPIRED", "token expired")
	ErrForbidden    = Err(http.StatusForbidden, "FORBIDDEN", "forbidden")
	ErrNotFound     = Err(http.StatusNotFound, "NOT_FOUND", "not found")
	ErrInvalidInput = Err(http.StatusBadRequest, "INVALID_INPUT", "invalid input")
	ErrConflict     = Err(http.StatusConflict, "CONFLICT", "conflict")
	ErrRateLimited  = Err(http.StatusTooManyRequests, "RATE_LIMITED", "rate limited")
	ErrInternal     = Err(http.StatusInternalServerError, "INTERNAL", "internal error")
	ErrUnavailable  = Err(http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable")
)

type errorEnvelope struct {
	Error *APIError `json:"error"`
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, err error) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		apiErr = ErrInternal
	}
	if apiErr.Status == 0 {
		apiErr.Status = http.StatusInternalServerError
	}
	WriteJSON(w, apiErr.Status, errorEnvelope{Error: apiErr})
}

func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil {
		return ErrInvalidInput
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid JSON body")
	}
	if dec.More() {
		return Err(http.StatusBadRequest, "INVALID_PARAMS", "unexpected trailing data")
	}
	return nil
}

func DecodeJSONLoose(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil {
		return ErrInvalidInput
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	if err := dec.Decode(dst); err != nil {
		return Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid JSON body")
	}
	return nil
}

func OK(w http.ResponseWriter, v any) { WriteJSON(w, http.StatusOK, v) }

func Created(w http.ResponseWriter, v any) { WriteJSON(w, http.StatusCreated, v) }
