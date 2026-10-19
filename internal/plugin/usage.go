package plugin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const (
	maxUsageBatch        = 100
	usageFutureTolerance = 5 * time.Minute
	usagePastTolerance   = 365 * 24 * time.Hour
)

type usageBody struct {
	Events []usageEventInput `json:"events"`
}

func (m *Module) postUsageEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := httpapi.PrincipalFrom(ctx)
	mm := m.loadMembership(ctx, p)
	prefix, err := pluginIDFromPath(chi.URLParam(r, "pluginId"))
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}

	// Resolve the plugin first so an unknown plugin is a 404, not a batch of
	// per-event rejections.
	var pluginID string
	visibility := "p.scope = 'public'"
	args := []any{prefix}
	if mm.OrgID != nil && *mm.OrgID != "" {
		args = append(args, *mm.OrgID)
		visibility = "(p.scope = 'public' OR (p.scope = 'organization' AND p.org_id = $2::uuid))"
	}
	if err := m.deps.DB.Pool.QueryRow(ctx, `
		SELECT p.id::text FROM plugin_plugins p
		WHERE p.status = 'active' AND p.id::text LIKE $1 || '%' AND `+visibility+`
		LIMIT 1`, args...).Scan(&pluginID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			notFound(w, "NOT_FOUND", "plugin not found")
			return
		}
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}

	var body usageBody
	if err := httpapi.DecodeJSON(w, r, &body); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if len(body.Events) == 0 {
		invalidInput(w, "INVALID_PARAMS", "events must not be empty")
		return
	}
	if len(body.Events) > maxUsageBatch {
		httpapi.WriteError(w, httpapi.Err(http.StatusRequestEntityTooLarge, "BATCH_TOO_LARGE", "at most 100 events per batch"))
		return
	}
	seen := make(map[string]struct{}, len(body.Events))
	for _, event := range body.Events {
		if _, dup := seen[event.EventID]; dup {
			invalidInput(w, "INVALID_PARAMS", "duplicate eventId in batch")
			return
		}
		seen[event.EventID] = struct{}{}
	}

	delivered, err := m.deliveredReleases(ctx, pluginID)
	if err != nil {
		unavailable(w, "USAGE_STORAGE_UNAVAILABLE", "usage storage unavailable")
		return
	}

	ack := usageAck{Accepted: []string{}, Duplicate: []string{}, Rejected: []usageRejected{}}
	now := time.Now().UTC()

	var tx pgx.Tx
	tx, err = m.deps.DB.Pool.Begin(ctx)
	if err != nil {
		unavailable(w, "USAGE_STORAGE_UNAVAILABLE", "usage storage unavailable")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, event := range body.Events {
		eventID := strings.TrimSpace(event.EventID)
		if eventID == "" || len(eventID) > 256 {
			ack.Rejected = append(ack.Rejected, usageRejected{EventID: event.EventID, Code: "INVALID_EVENT_ID"})
			continue
		}
		releaseID, parseErr := uuid.Parse(strings.TrimSpace(event.ReleaseID))
		if parseErr != nil {
			ack.Rejected = append(ack.Rejected, usageRejected{EventID: event.EventID, Code: "RELEASE_NOT_DELIVERED"})
			continue
		}
		if _, ok := delivered[releaseID.String()]; !ok {
			ack.Rejected = append(ack.Rejected, usageRejected{EventID: event.EventID, Code: "RELEASE_NOT_DELIVERED"})
			continue
		}
		occurredAt, timeErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(event.OccurredAt))
		if timeErr != nil || occurredAt.After(now.Add(usageFutureTolerance)) || occurredAt.Before(now.Add(-usagePastTolerance)) {
			ack.Rejected = append(ack.Rejected, usageRejected{EventID: event.EventID, Code: "OCCURRED_AT_OUT_OF_RANGE"})
			continue
		}

		var inserted string
		insertErr := tx.QueryRow(ctx, `
			INSERT INTO plugin_usage_events (event_id, plugin_id, release_id, account_id, occurred_at)
			VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5)
			ON CONFLICT (event_id) DO NOTHING
			RETURNING id::text`,
			eventID, pluginID, releaseID.String(), ticketAccount(p.AccountID), occurredAt).Scan(&inserted)
		if insertErr != nil {
			if !errors.Is(insertErr, pgx.ErrNoRows) {
				unavailable(w, "USAGE_STORAGE_UNAVAILABLE", "usage storage unavailable")
				return
			}
			// Existing event id: same facts are duplicates, different facts conflict.
			var existingPlugin, existingRelease string
			var existingAt time.Time
			if err := tx.QueryRow(ctx, `
				SELECT plugin_id::text, release_id::text, occurred_at
				FROM plugin_usage_events WHERE event_id = $1`, eventID).
				Scan(&existingPlugin, &existingRelease, &existingAt); err != nil {
				unavailable(w, "USAGE_STORAGE_UNAVAILABLE", "usage storage unavailable")
				return
			}
			same := existingPlugin == pluginID &&
				existingRelease == releaseID.String() &&
				existingAt.UTC().Truncate(time.Microsecond).Equal(occurredAt.UTC().Truncate(time.Microsecond))
			if same {
				ack.Duplicate = append(ack.Duplicate, eventID)
			} else {
				ack.Rejected = append(ack.Rejected, usageRejected{EventID: eventID, Code: "EVENT_ID_CONFLICT"})
			}
			continue
		}

		day := time.Date(occurredAt.UTC().Year(), occurredAt.UTC().Month(), occurredAt.UTC().Day(), 0, 0, 0, 0, time.UTC)
		if _, err := tx.Exec(ctx, `
			INSERT INTO plugin_usage_daily (plugin_id, release_id, day, event_count)
			VALUES ($1::uuid, $2::uuid, $3::date, 1)
			ON CONFLICT (plugin_id, release_id, day)
			DO UPDATE SET event_count = plugin_usage_daily.event_count + 1`,
			pluginID, releaseID.String(), day); err != nil {
			unavailable(w, "USAGE_STORAGE_UNAVAILABLE", "usage storage unavailable")
			return
		}
		ack.Accepted = append(ack.Accepted, eventID)
	}

	if err := tx.Commit(ctx); err != nil {
		unavailable(w, "USAGE_STORAGE_UNAVAILABLE", "usage storage unavailable")
		return
	}
	httpapi.OK(w, ack)
}

func (m *Module) deliveredReleases(ctx context.Context, pluginID string) (map[string]struct{}, error) {
	rows, err := m.deps.DB.Pool.Query(ctx, `
		SELECT id::text FROM plugin_releases
		WHERE plugin_id = $1::uuid AND review_status = 'approved'`, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}
