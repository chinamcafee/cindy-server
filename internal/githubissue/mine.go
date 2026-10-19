package githubissue

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type myIssueItem struct {
	Number    int    `json:"number"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	State     string `json:"state"`
	CreatedAt string `json:"createdAt"`
}

type myIssuesResponse struct {
	Issues []myIssueItem `json:"issues"`
}

// myIssues returns only issues submitted by the current account. Local ledger
// rows are the source of truth; nothing is fetched from other users.
func (m *Module) myIssues(w http.ResponseWriter, r *http.Request) {
	if m.deps.DB == nil {
		httpapi.WriteError(w, httpapi.ErrUnavailable)
		return
	}
	principal := httpapi.MustPrincipal(r.Context())
	if _, err := uuid.Parse(principal.AccountID); err != nil {
		httpapi.WriteError(w, httpapi.ErrInvalidToken)
		return
	}

	rows, err := m.deps.DB.Pool.Query(r.Context(), `
		SELECT github_number, COALESCE(url, ''), title, status, created_at
		FROM gh_issues
		WHERE account_id = $1::uuid AND github_number IS NOT NULL
		ORDER BY created_at DESC
		LIMIT 50`, principal.AccountID)
	if err != nil {
		m.log.Error("github_issues_mine_failed", "account_id", principal.AccountID, "error", err)
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	defer rows.Close()

	issues := make([]myIssueItem, 0, 16)
	for rows.Next() {
		var (
			item      myIssueItem
			createdAt time.Time
		)
		if err := rows.Scan(&item.Number, &item.URL, &item.Title, &item.Status, &createdAt); err != nil {
			m.log.Error("github_issues_mine_scan_failed", "error", err)
			httpapi.WriteError(w, httpapi.ErrInternal)
			return
		}
		item.State = item.Status
		item.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		issues = append(issues, item)
	}
	if err := rows.Err(); err != nil {
		m.log.Error("github_issues_mine_rows_failed", "error", err)
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}

	httpapi.OK(w, myIssuesResponse{Issues: issues})
}
