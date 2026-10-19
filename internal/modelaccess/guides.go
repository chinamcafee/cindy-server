package modelaccess

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type resolvedGuide struct {
	ModelID string         `json:"modelId"`
	Guide   map[string]any `json:"guide"`
}

func (m *Module) loadGuide(r *http.Request, modelID string) (*resolvedGuide, error) {
	if m.db == nil || modelID == "" || len(modelID) > 256 {
		return nil, nil
	}
	var guideID, key string
	var raw []byte
	var rev int
	err := m.db.Pool.QueryRow(r.Context(), `
		SELECT g.id::text, g.guide, g.revision, mm.model_key
		FROM ma_guides g
		JOIN ma_models mm ON mm.id = g.model_id
		WHERE mm.model_key = $1
		LIMIT 1`, modelID).Scan(&guideID, &raw, &rev, &key)
	if err != nil {
		return nil, nil
	}
	var guide map[string]any
	if err := json.Unmarshal(raw, &guide); err != nil {
		return nil, err
	}
	if guide == nil {
		guide = map[string]any{}
	}
	guide["guideId"] = guideID
	guide["revision"] = rev
	return &resolvedGuide{ModelID: key, Guide: guide}, nil
}

func (m *Module) invocationGuide(w http.ResponseWriter, r *http.Request) {
	modelID := strings.TrimSpace(r.URL.Query().Get("modelId"))
	if modelID == "" || len(modelID) > 256 {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "modelId is required"))
		return
	}
	modelID = modelID[strings.LastIndex(modelID, "/")+1:]
	guide, err := m.loadGuide(r, modelID)
	if err != nil {
		httpapi.WriteError(w, httpapi.Err(http.StatusInternalServerError, "GUIDE_INVALID", "guide payload is invalid"))
		return
	}
	if guide == nil {
		httpapi.WriteError(w, notFound("MEDIA_INVOCATION_GUIDE_NOT_FOUND", "invocation guide not found"))
		return
	}
	httpapi.OK(w, guide)
}

func (m *Module) invocationGuides(w http.ResponseWriter, r *http.Request) {
	guides := make([]resolvedGuide, 0, 16)
	if m.db != nil {
		rows, err := m.db.Pool.Query(r.Context(), `
			SELECT g.id::text, g.guide, g.revision, mm.model_key
			FROM ma_guides g
			JOIN ma_models mm ON mm.id = g.model_id
			JOIN ma_providers p ON p.id = mm.provider_id
			WHERE p.status = 'active'
			ORDER BY mm.model_key
			LIMIT 256`)
		if err != nil {
			httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "GUIDE_SERVICE_UNAVAILABLE", "guides unavailable"))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var guideID, key string
			var raw []byte
			var rev int
			if err := rows.Scan(&guideID, &raw, &rev, &key); err != nil {
				httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "GUIDE_SERVICE_UNAVAILABLE", "guides unavailable"))
				return
			}
			var guide map[string]any
			if err := json.Unmarshal(raw, &guide); err != nil || guide == nil {
				continue
			}
			guide["guideId"] = guideID
			guide["revision"] = rev
			guides = append(guides, resolvedGuide{ModelID: key, Guide: guide})
		}
		if err := rows.Err(); err != nil {
			httpapi.WriteError(w, httpapi.Err(http.StatusServiceUnavailable, "GUIDE_SERVICE_UNAVAILABLE", "guides unavailable"))
			return
		}
	}
	httpapi.OK(w, map[string]any{"guides": guides})
}
