package admin

import (
	"net/http"
	"strings"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type timeseriesPoint struct {
	Date  string `json:"date"`
	Value int64  `json:"value"`
}

func (m *Module) dashboardSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var accounts, activeAccounts, devices, messages24h, plugins, skills, revenueMinor, audit24h int64
	err := m.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM auth_accounts WHERE status <> 'deleted'),
			(SELECT count(*) FROM auth_accounts WHERE status = 'active'),
			(SELECT count(*) FROM dl_devices WHERE removed_at IS NULL),
			(SELECT count(*) FROM chat_messages WHERE deleted_at IS NULL AND created_at >= now() - interval '24 hours'),
			(SELECT count(*) FROM plugin_plugins WHERE status = 'active'),
			(SELECT count(*) FROM sk_skills WHERE status = 'active'),
			(SELECT COALESCE(sum(amount_minor), 0) FROM ma_orders WHERE status = 'paid'),
			(SELECT count(*) FROM audit_events WHERE occurred_at >= now() - interval '24 hours')
	`).Scan(&accounts, &activeAccounts, &devices, &messages24h, &plugins, &skills, &revenueMinor, &audit24h)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	online := int64(len(m.onlineDeviceSet(ctx)))
	httpapi.OK(w, map[string]any{
		"accounts":       accounts,
		"activeAccounts": activeAccounts,
		"devices":        devices,
		"onlineDevices":  online,
		"messages24h":    messages24h,
		"plugins":        plugins,
		"skills":         skills,
		"revenueMinor":   revenueMinor,
		"audit24h":       audit24h,
	})
}

func (m *Module) dashboardTimeseries(w http.ResponseWriter, r *http.Request) {
	metric := strings.TrimSpace(r.URL.Query().Get("metric"))
	if metric == "" {
		metric = "accounts"
	}
	if metric != "accounts" && metric != "messages" {
		m.badRequest(w, "INVALID_METRIC", "metric must be accounts or messages")
		return
	}
	days := queryInt(r, "days", 7)
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}

	var query string
	switch metric {
	case "accounts":
		query = `
			WITH days AS (
				SELECT generate_series(current_date - ($1::int - 1), current_date, interval '1 day')::date AS day
			)
			SELECT to_char(d.day, 'YYYY-MM-DD'), COALESCE(c.cnt, 0)
			FROM days d
			LEFT JOIN (
				SELECT date(created_at) AS day, count(*) AS cnt
				FROM auth_accounts
				WHERE created_at >= current_date - ($1::int - 1)
				GROUP BY 1
			) c ON c.day = d.day
			ORDER BY d.day`
	case "messages":
		query = `
			WITH days AS (
				SELECT generate_series(current_date - ($1::int - 1), current_date, interval '1 day')::date AS day
			)
			SELECT to_char(d.day, 'YYYY-MM-DD'), COALESCE(c.cnt, 0)
			FROM days d
			LEFT JOIN (
				SELECT date(created_at) AS day, count(*) AS cnt
				FROM chat_messages
				WHERE deleted_at IS NULL AND created_at >= current_date - ($1::int - 1)
				GROUP BY 1
			) c ON c.day = d.day
			ORDER BY d.day`
	}

	rows, err := m.pool.Query(r.Context(), query, days)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer rows.Close()
	points := make([]timeseriesPoint, 0, days)
	for rows.Next() {
		var p timeseriesPoint
		if err := rows.Scan(&p.Date, &p.Value); err != nil {
			m.fail(w, r, err)
			return
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		m.fail(w, r, err)
		return
	}
	httpapi.OK(w, map[string]any{"points": points})
}
