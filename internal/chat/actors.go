package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type createActorRequest struct {
	OperationID string `json:"operationId"`
	Kind        string `json:"kind"`
	ExternalID  string `json:"externalId"`
	Name        string `json:"name"`
}

type patchActorRequest struct {
	OperationID          string          `json:"operationId"`
	HomeDeviceID         json.RawMessage `json:"homeDeviceId"`
	ExpectedHomeDeviceID json.RawMessage `json:"expectedHomeDeviceId"`
	Disabled             *bool           `json:"disabled"`
}

type actorProfileRequest struct {
	OperationID  string  `json:"operationId"`
	Name         *string `json:"name"`
	Avatar       *string `json:"avatar"`
	AvatarSource *string `json:"avatarSource"`
}

type preferenceWriteRequest struct {
	OperationID      string          `json:"operationId"`
	Key              string          `json:"key"`
	Value            json.RawMessage `json:"value"`
	ExpectedRevision int             `json:"expectedRevision"`
}

func (m *Module) handleMe(w http.ResponseWriter, r *http.Request) {
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		m.fail(w, err)
		return
	}
	resp := map[string]any{
		"actor": map[string]any{"id": human.ID, "kind": human.Kind},
		"capabilities": map[string]any{
			"groupDiscussionParity":        true,
			"companionHome":                true,
			"directRoomClaim":              true,
			"groupLifecycleVersion":        1,
			"conversationListStateVersion": 1,
		},
	}
	dev := deviceID(r)
	if dev == "" {
		if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
			if claims, err := m.deps.Tokens.ParseAccess(strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))); err == nil {
				dev = claims.DeviceID
			}
		}
	}
	if dev != "" {
		resp["deviceId"] = dev
	}
	httpapi.OK(w, resp)
}

func (m *Module) handleProfileRefresh(w http.ResponseWriter, r *http.Request) {
	if _, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID); err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, map[string]any{})
}

func (m *Module) handleListActors(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r, 100)
	if err != nil {
		m.fail(w, err)
		return
	}
	accountID := httpapi.MustPrincipal(r.Context()).AccountID
	rows, err := m.deps.DB.Pool.Query(r.Context(), `SELECT `+actorCols+` FROM chat_actors WHERE account_id=$1 ORDER BY (kind='human') DESC, created_at ASC LIMIT $2`, accountID, limit)
	if err != nil {
		m.fail(w, err)
		return
	}
	defer rows.Close()
	out := []actorWire{}
	for rows.Next() {
		a, err := scanActor(rows)
		if err != nil {
			m.fail(w, err)
			return
		}
		out = append(out, a.wire())
	}
	if err := rows.Err(); err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, out)
}

func (m *Module) handleCreateActor(w http.ResponseWriter, r *http.Request) {
	var req createActorRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	accountID := httpapi.MustPrincipal(r.Context()).AccountID
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		if req.Kind != "bot" {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "kind must be bot")
		}
		externalID := strings.TrimSpace(req.ExternalID)
		name := strings.TrimSpace(req.Name)
		if externalID == "" || len(externalID) > 160 || name == "" || len(name) > 80 {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "externalId and name are required")
		}
		var a actorRow
		var err error
		row := m.deps.DB.Pool.QueryRow(ctx, `
			INSERT INTO chat_actors (account_id, kind, external_id, display_name)
			VALUES ($1, 'bot', $2, $3)
			ON CONFLICT (account_id, external_id) WHERE external_id IS NOT NULL DO NOTHING
			RETURNING `+actorCols, accountID, externalID, name)
		a, err = scanActor(row)
		if errors.Is(err, pgx.ErrNoRows) {
			a, err = scanActor(m.deps.DB.Pool.QueryRow(ctx, `SELECT `+actorCols+` FROM chat_actors WHERE account_id=$1 AND external_id=$2`, accountID, externalID))
		}
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, a.wire(), nil
	})
}

func rawString(raw json.RawMessage) (value *string, present bool, err error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, true, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "expected string or null")
	}
	return &s, true, nil
}

func (m *Module) handlePatchActor(w http.ResponseWriter, r *http.Request) {
	var req patchActorRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	accountID := httpapi.MustPrincipal(r.Context()).AccountID
	actorID := chi.URLParam(r, "actorId")
	m.idemDoOptional(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		actor, err := m.loadActor(ctx, actorID)
		if err != nil || actor.AccountID != accountID {
			return 0, nil, errActorNotOwned
		}
		newHome, homePresent, err := rawString(req.HomeDeviceID)
		if err != nil {
			return 0, nil, err
		}
		expected, expectedPresent, err := rawString(req.ExpectedHomeDeviceID)
		if err != nil {
			return 0, nil, err
		}
		if homePresent && !expectedPresent {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "expectedHomeDeviceId is required")
		}
		if !homePresent && req.Disabled == nil {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "no fields to update")
		}
		if !expectedPresent {
			expected = actor.HomeDeviceID
		}
		targetHome := actor.HomeDeviceID
		if homePresent {
			targetHome = newHome
		}
		var updated actorRow
		row := m.deps.DB.Pool.QueryRow(ctx, `
			UPDATE chat_actors
			SET home_device_id=$1, disabled=COALESCE($2, disabled), updated_at=now()
			WHERE id=$3 AND account_id=$4 AND home_device_id IS NOT DISTINCT FROM $5
			RETURNING `+actorCols, targetHome, req.Disabled, actorID, accountID, expected)
		updated, err = scanActor(row)
		if errors.Is(err, pgx.ErrNoRows) {
			current, loadErr := m.loadActor(ctx, actorID)
			if loadErr != nil {
				return 0, nil, loadErr
			}
			return 0, nil, homeConflict(current.HomeDeviceID)
		}
		if err != nil {
			return 0, nil, err
		}
		if homePresent {
			changed := (actor.HomeDeviceID == nil) != (updated.HomeDeviceID == nil)
			if !changed && actor.HomeDeviceID != nil && updated.HomeDeviceID != nil {
				changed = *actor.HomeDeviceID != *updated.HomeDeviceID
			}
			if changed {
				if _, err := m.deps.DB.Pool.Exec(ctx, `
					UPDATE chat_executions SET epoch=epoch+1, status='stopped', lease_expires_at=NULL, updated_at=now()
					WHERE bot_actor_id=$1 AND status IN ('queued','claimed','running')`, actorID); err != nil {
					return 0, nil, err
				}
			}
		}
		if updated.Disabled && !actor.Disabled {
			if _, err := m.deps.DB.Pool.Exec(ctx, `
				UPDATE chat_executions SET epoch=epoch+1, status='stopped', lease_expires_at=NULL, updated_at=now()
				WHERE bot_actor_id=$1 AND status IN ('queued','claimed','running')`, actorID); err != nil {
				return 0, nil, err
			}
		}
		return http.StatusOK, map[string]any{"homeDeviceId": updated.HomeDeviceID, "disabled": updated.Disabled}, nil
	})
}

func (m *Module) handleActorProfile(w http.ResponseWriter, r *http.Request) {
	var req actorProfileRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	accountID := httpapi.MustPrincipal(r.Context()).AccountID
	actorID := chi.URLParam(r, "actorId")
	m.idemDoOptional(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		actor, err := m.loadActor(ctx, actorID)
		if err != nil || actor.AccountID != accountID {
			return 0, nil, errActorNotOwned
		}
		if req.Name == nil && req.Avatar == nil && req.AvatarSource == nil {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "no profile fields")
		}
		if actor.HomeDeviceID != nil {
			if dev := deviceID(r); dev != "" && dev != *actor.HomeDeviceID {
				return 0, nil, httpapi.Err(http.StatusForbidden, "FORBIDDEN", "home device required")
			}
		}
		name := actor.DisplayName
		if req.Name != nil {
			name = strings.TrimSpace(*req.Name)
			if name == "" || len(name) > 80 {
				return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid name")
			}
		}
		var updated actorRow
		row := m.deps.DB.Pool.QueryRow(ctx, `
			UPDATE chat_actors SET display_name=$1, avatar_url=COALESCE($2, avatar_url),
				avatar_source=COALESCE($3, avatar_source), updated_at=now()
			WHERE id=$4 RETURNING `+actorCols, name, req.Avatar, req.AvatarSource, actorID)
		updated, err = scanActor(row)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, updated.wire(), nil
	})
}

func (m *Module) preferenceActor(r *http.Request) (actorRow, error) {
	human, err := m.ensureHumanActor(r.Context(), httpapi.MustPrincipal(r.Context()).AccountID)
	if err != nil {
		return actorRow{}, err
	}
	actor, err := m.resolveActingActor(r, human)
	if err != nil {
		return actorRow{}, err
	}
	if actor.Kind != "bot" {
		return actorRow{}, errActorNotOwned
	}
	return actor, nil
}

type preferenceWire struct {
	Key      string          `json:"key"`
	Value    json.RawMessage `json:"value"`
	Revision int             `json:"revision"`
}

func (m *Module) handleListPreferences(w http.ResponseWriter, r *http.Request) {
	actor, err := m.preferenceActor(r)
	if err != nil {
		m.fail(w, err)
		return
	}
	limit, err := parseLimit(r, 100)
	if err != nil {
		m.fail(w, err)
		return
	}
	rows, err := m.deps.DB.Pool.Query(r.Context(), `
		SELECT key, CASE WHEN deleted_at IS NULL THEN value ELSE NULL END, revision
		FROM chat_preferences WHERE actor_id=$1 ORDER BY key LIMIT $2`, actor.ID, limit)
	if err != nil {
		m.fail(w, err)
		return
	}
	defer rows.Close()
	out := []preferenceWire{}
	for rows.Next() {
		var item preferenceWire
		var raw []byte
		if err := rows.Scan(&item.Key, &raw, &item.Revision); err != nil {
			m.fail(w, err)
			return
		}
		item.Value = json.RawMessage(raw)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		m.fail(w, err)
		return
	}
	httpapi.OK(w, out)
}

func (m *Module) handleWritePreference(w http.ResponseWriter, r *http.Request) {
	var req preferenceWriteRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	actor, err := m.preferenceActor(r)
	if err != nil {
		m.fail(w, err)
		return
	}
	m.idemDo(w, r, req.OperationID, req, func(ctx context.Context) (int, any, error) {
		key := strings.TrimSpace(req.Key)
		if key == "" || len(key) > 160 {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid key")
		}
		if len(req.Value) > 60*1024 {
			return 0, nil, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "value too large")
		}
		tombstone := len(req.Value) == 0 || string(req.Value) == "null"
		var value any
		if !tombstone {
			value = []byte(req.Value)
		}
		var deletedAt any
		if tombstone {
			deletedAt = time.Now().UTC()
		}
		var revision int
		var err error
		if req.ExpectedRevision == 0 {
			err = m.deps.DB.Pool.QueryRow(ctx, `
				INSERT INTO chat_preferences (actor_id, key, value, revision, deleted_at, updated_at)
				VALUES ($1,$2,$3::jsonb,1,$4,now())
				ON CONFLICT (actor_id, key) DO NOTHING
				RETURNING revision`, actor.ID, key, value, deletedAt).Scan(&revision)
			if errors.Is(err, pgx.ErrNoRows) {
				var current int
				readErr := m.deps.DB.Pool.QueryRow(ctx, `SELECT revision FROM chat_preferences WHERE actor_id=$1 AND key=$2`, actor.ID, key).Scan(&current)
				if readErr != nil {
					return 0, nil, readErr
				}
				return 0, nil, revisionConflict(current)
			}
		} else {
			err = m.deps.DB.Pool.QueryRow(ctx, `
				UPDATE chat_preferences SET value=$3::jsonb, deleted_at=$4, revision=revision+1, updated_at=now()
				WHERE actor_id=$1 AND key=$2 AND revision=$5
				RETURNING revision`, actor.ID, key, value, deletedAt, req.ExpectedRevision).Scan(&revision)
			if errors.Is(err, pgx.ErrNoRows) {
				var current int
				readErr := m.deps.DB.Pool.QueryRow(ctx, `SELECT revision FROM chat_preferences WHERE actor_id=$1 AND key=$2`, actor.ID, key).Scan(&current)
				if errors.Is(readErr, pgx.ErrNoRows) {
					current = 0
				} else if readErr != nil {
					return 0, nil, readErr
				}
				return 0, nil, revisionConflict(current)
			}
		}
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{"revision": revision}, nil
	})
}
