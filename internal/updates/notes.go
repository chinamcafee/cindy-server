package updates

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type announcementItem struct {
	Platform    string `json:"platform"`
	Version     string `json:"version,omitempty"`
	Locale      string `json:"locale"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	PublishedAt string `json:"publishedAt,omitempty"`
}

type noteTopic struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

type releaseNotesResponse struct {
	Platform      string             `json:"platform"`
	Version       string             `json:"version"`
	Date          string             `json:"date,omitempty"`
	Announcements []announcementItem `json:"announcements"`
	// Desktop release-notes dialog consumes the CDN-style topic list. Keep the
	// announcement list for admin/debug consumers and mirror it into topics.
	Topics []noteTopic `json:"topics,omitempty"`
}

type announcementsResponse struct {
	Announcements []announcementItem `json:"announcements"`
}

// releaseNotes returns all published announcements for one platform key and
// version, grouped in the update-notice shape used by the desktop client.
func (m *Module) releaseNotes(w http.ResponseWriter, r *http.Request) {
	platform := strings.TrimSpace(chi.URLParam(r, "platform"))
	version := strings.TrimSuffix(strings.TrimSpace(chi.URLParam(r, "version")), ".json")
	if platform == "" || version == "" {
		httpapi.WriteError(w, httpapi.ErrInvalidInput)
		return
	}
	if m.deps.DB == nil {
		httpapi.WriteError(w, httpapi.ErrNotFound)
		return
	}
	if version == "index" {
		m.releaseNotesIndex(w, r, platform)
		return
	}

	rows, err := m.deps.DB.Pool.Query(r.Context(), `
		SELECT platform, COALESCE(version, ''), locale, title, body,
		       COALESCE(published_at, created_at)
		FROM announcements
		WHERE status = 'published' AND platform = $1 AND version = $2
		ORDER BY locale, created_at DESC
		LIMIT 20`, platform, version)
	if err != nil {
		m.log.Error("release_notes_lookup_failed", "platform", platform, "version", version, "error", err)
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	defer rows.Close()

	items, newest, err := scanAnnouncements(rows)
	if err != nil {
		m.log.Error("release_notes_scan_failed", "platform", platform, "version", version, "error", err)
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	if len(items) == 0 {
		httpapi.WriteError(w, httpapi.Err(http.StatusNotFound, "NOT_FOUND", "release notes not found"))
		return
	}

	resp := releaseNotesResponse{Platform: platform, Version: version, Announcements: items}
	if !newest.IsZero() {
		// The desktop parser requires a millisecond-precision UTC timestamp.
		resp.Date = newest.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		text := strings.TrimSpace(item.Body)
		if title == "" || text == "" {
			continue
		}
		resp.Topics = append(resp.Topics, noteTopic{ID: item.Platform + ":" + item.Version + ":" + item.Locale, Title: title, Text: text})
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	httpapi.OK(w, resp)
}

func (m *Module) releaseNotesIndex(w http.ResponseWriter, r *http.Request, platform string) {
	rows, err := m.deps.DB.Pool.Query(r.Context(), `
		SELECT DISTINCT version FROM announcements
		WHERE status = 'published' AND platform = $1 AND version IS NOT NULL AND version <> ''
		ORDER BY version`, platform)
	if err != nil {
		m.log.Error("release_notes_index_failed", "platform", platform, "error", err)
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	defer rows.Close()
	versions := []string{}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			m.log.Error("release_notes_index_scan_failed", "platform", platform, "error", err)
			httpapi.WriteError(w, httpapi.ErrInternal)
			return
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		m.log.Error("release_notes_index_rows_failed", "platform", platform, "error", err)
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	httpapi.OK(w, versions)
}

// announcements lists published announcements using optional filters. Unlike
// releaseNotes an empty result is a valid answer.
func (m *Module) announcements(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	platform := strings.TrimSpace(q.Get("platform"))
	version := strings.TrimSpace(q.Get("version"))
	locale := strings.TrimSpace(q.Get("locale"))

	args := make([]any, 0, 3)
	where := []string{"status = 'published'"}
	if platform != "" {
		args = append(args, platform)
		where = append(where, fmt.Sprintf("platform = $%d", len(args)))
	}
	if version != "" {
		args = append(args, version)
		where = append(where, fmt.Sprintf("version = $%d", len(args)))
	}
	if locale != "" {
		args = append(args, locale)
		where = append(where, fmt.Sprintf("locale = $%d", len(args)))
	}

	items := []announcementItem{}
	if m.deps.DB != nil {
		query := `
			SELECT platform, COALESCE(version, ''), locale, title, body,
			       COALESCE(published_at, created_at)
			FROM announcements
			WHERE ` + strings.Join(where, " AND ") + `
			ORDER BY COALESCE(published_at, created_at) DESC, created_at DESC
			LIMIT 50`
		rows, err := m.deps.DB.Pool.Query(r.Context(), query, args...)
		if err != nil {
			m.log.Error("announcements_lookup_failed", "error", err)
			httpapi.WriteError(w, httpapi.ErrInternal)
			return
		}
		defer rows.Close()

		scanned, _, err := scanAnnouncements(rows)
		if err != nil {
			m.log.Error("announcements_scan_failed", "error", err)
			httpapi.WriteError(w, httpapi.ErrInternal)
			return
		}
		items = scanned
	}

	w.Header().Set("Cache-Control", "public, max-age=60")
	httpapi.OK(w, announcementsResponse{Announcements: items})
}

type announcementRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanAnnouncements(rows announcementRows) ([]announcementItem, time.Time, error) {
	items := make([]announcementItem, 0, 8)
	var newest time.Time
	for rows.Next() {
		var (
			item      announcementItem
			published time.Time
		)
		if err := rows.Scan(&item.Platform, &item.Version, &item.Locale, &item.Title, &item.Body, &published); err != nil {
			return nil, time.Time{}, err
		}
		item.PublishedAt = published.UTC().Format(time.RFC3339)
		if published.After(newest) {
			newest = published
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return items, newest, nil
}
