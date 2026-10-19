package modelaccess

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const decimalScale = 9

func chiURLParam(r *http.Request, key string) string {
	return chi.URLParam(r, key)
}

func isUUID(v string) bool {
	_, err := uuid.Parse(v)
	return err == nil
}

func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func isoPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := isoMillis(*t)
	return &s
}

func isoMillisPtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := isoMillis(t)
	return &s
}

// amountString renders integer minor units as a decimal major-unit string.
func amountString(minor int64) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	s := fmt.Sprintf("%d.%02d", minor/100, minor%100)
	if neg {
		return "-" + s
	}
	return s
}

// creditString renders a credit-ledger integer as a decimal string at scale 9.
func creditString(v int64) string {
	return strconv.FormatInt(v, 10)
}

func parseDecimalNumber(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func badRequest(code, message string) *httpapi.APIError {
	return httpapi.Err(http.StatusBadRequest, code, message)
}

func notFound(code, message string) *httpapi.APIError {
	return httpapi.Err(http.StatusNotFound, code, message)
}

func conflict(code, message string) *httpapi.APIError {
	return httpapi.Err(http.StatusConflict, code, message)
}

func unavailable(code, message string) *httpapi.APIError {
	return httpapi.Err(http.StatusServiceUnavailable, code, message)
}

func notImplemented(code, message string) *httpapi.APIError {
	return httpapi.Err(http.StatusNotImplemented, code, message)
}

func clampNonNegative(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
