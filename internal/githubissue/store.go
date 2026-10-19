package githubissue

import (
	"context"
	"fmt"
)

const (
	// mockNumberBase keeps development issue numbers clearly out of the real
	// GitHub range so they cannot be mistaken for live issues.
	mockNumberBase = 900000
)

// insertMockIssue allocates the next mock number and records the issue in the
// local ledger. There is no unique constraint on github_number, so the
// statement-level MAX snapshot is sufficient for development use.
func (m *Module) insertMockIssue(ctx context.Context, accountID, issueType, title, description, appVersion string) (issueRef, error) {
	urlPrefix := fmt.Sprintf("https://github.com/%s/%s/issues/", githubRepoOwner, githubRepoName)
	var ref issueRef
	err := m.deps.DB.Pool.QueryRow(ctx, `
		WITH next AS (
			SELECT COALESCE(MAX(github_number), $7) + 1 AS num FROM gh_issues
		)
		INSERT INTO gh_issues (account_id, type, title, description, app_version, github_number, url, status)
		SELECT $1::uuid, $2, $3, NULLIF($4, ''), NULLIF($5, ''), next.num, $6 || next.num, 'submitted'
		FROM next
		RETURNING github_number, COALESCE(url, '')`,
		accountID, issueType, title, description, appVersion, urlPrefix, mockNumberBase,
	).Scan(&ref.Number, &ref.URL)
	if err != nil {
		return issueRef{}, fmt.Errorf("insert mock issue: %w", err)
	}
	return ref, nil
}

// insertIssue stores a real GitHub issue reference for /mine lookups.
func (m *Module) insertIssue(ctx context.Context, accountID, issueType, title, description, appVersion string, ref issueRef) error {
	_, err := m.deps.DB.Pool.Exec(ctx, `
		INSERT INTO gh_issues (account_id, type, title, description, app_version, github_number, url, status)
		VALUES ($1::uuid, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, NULLIF($7, ''), 'submitted')`,
		accountID, issueType, title, description, appVersion, ref.Number, ref.URL)
	if err != nil {
		return fmt.Errorf("insert issue: %w", err)
	}
	return nil
}
