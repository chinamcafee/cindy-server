package githubissue

import (
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const (
	maxTitleRunes       = 200
	maxDescriptionRunes = 5000
	maxAppVersionRunes  = 64
	maxUserNameRunes    = 64
)

// publicNamePattern allows letters (incl. CJK), digits, spaces and a few safe
// separators, blocking Markdown/HTML injection and impersonation tricks.
var publicNamePattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._-]*$`)

type createIssueRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Type        string `json:"type"`
	AppVersion  string `json:"appVersion"`
	UserName    string `json:"userName"`
}

type issueRef struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

type createIssueResponse struct {
	GitHubIssue issueRef `json:"githubIssue"`
}

// createIssue forwards a client-confirmed feedback report to GitHub. When no
// platform token is configured it records a mock issue so the desktop flow can
// be exercised end to end in development.
func (m *Module) createIssue(w http.ResponseWriter, r *http.Request) {
	if m.deps.DB == nil {
		httpapi.WriteError(w, httpapi.ErrUnavailable)
		return
	}

	var req createIssueRequest
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}

	title := strings.TrimSpace(req.Title)
	if title == "" || utf8.RuneCountInString(title) > maxTitleRunes {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_TITLE", "title must be 1-200 characters"))
		return
	}
	description := req.Description
	if utf8.RuneCountInString(description) > maxDescriptionRunes {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_DESCRIPTION", "description must be at most 5000 characters"))
		return
	}

	issueType := strings.ToLower(strings.TrimSpace(req.Type))
	if issueType != "bug" && issueType != "feature" {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_TYPE", "type must be bug or feature"))
		return
	}

	appVersion := strings.TrimSpace(req.AppVersion)
	if utf8.RuneCountInString(appVersion) > maxAppVersionRunes {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_APP_VERSION", "appVersion is too long"))
		return
	}

	principal := httpapi.MustPrincipal(r.Context())
	if _, err := uuid.Parse(principal.AccountID); err != nil {
		httpapi.WriteError(w, httpapi.ErrInvalidToken)
		return
	}

	userName := strings.TrimSpace(req.UserName)
	if userName != "" && !validPublicName(userName) {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_USER_NAME", "userName contains unsupported characters"))
		return
	}
	reporter := userName
	if reporter == "" {
		reporter = principal.MembershipID
	}

	// Defense in depth: the client redacts before sending, the server redacts
	// again so credentials never reach GitHub or the local ledger.
	title = redactSecrets(title)
	description = redactSecrets(description)
	repoBody := composeIssueBody(description, appVersion, reporter)

	var (
		ref  issueRef
		mock bool
		err  error
	)
	if strings.TrimSpace(m.githubToken()) == "" {
		mock = true
		ref, err = m.insertMockIssue(r.Context(), principal.AccountID, issueType, title, description, appVersion)
	} else {
		ref, err = m.postGitHubIssue(r.Context(), title, repoBody, issueType)
		if err == nil {
			// The issue already exists on GitHub; a ledger failure must not
			// turn a successful submission into a client-visible error.
			if insertErr := m.insertIssue(r.Context(), principal.AccountID, issueType, title, description, appVersion, ref); insertErr != nil {
				m.log.Error("github_issue_ledger_failed", "number", ref.Number, "error", insertErr)
			}
		}
	}
	if err != nil {
		m.log.Error("github_issue_create_failed", "type", issueType, "mock", mock, "error", err)
		httpapi.WriteError(w, httpapi.Err(http.StatusBadGateway, "GITHUB_UNAVAILABLE", "could not create issue"))
		return
	}

	// Never log the title/body: reports may contain pasted logs or paths.
	m.log.Info("github_issue_created",
		"number", ref.Number,
		"account_id", principal.AccountID,
		"type", issueType,
		"mock", mock,
	)
	if m.deps.Audit != nil {
		m.deps.Audit.Log(r.Context(), audit.Event{
			Action:     "github.issue.create",
			TargetType: "github_issue",
			TargetID:   refURLOrNumber(ref),
			After:      map[string]any{"type": issueType, "number": ref.Number, "mock": mock},
		})
	}

	httpapi.OK(w, createIssueResponse{GitHubIssue: ref})
}

func validPublicName(name string) bool {
	return utf8.RuneCountInString(name) <= maxUserNameRunes && publicNamePattern.MatchString(name)
}

// composeIssueBody keeps the user Markdown intact and appends a small
// attribution footer with the app version and confirmed public name.
func composeIssueBody(description, appVersion, reporter string) string {
	body := description
	if strings.TrimSpace(body) != "" {
		body += "\n\n"
	}
	body += "---\n"
	if appVersion != "" {
		body += "- app version: " + appVersion + "\n"
	}
	body += "- reported by: " + reporter + "\n"
	return body
}

func refURLOrNumber(ref issueRef) string {
	if ref.URL != "" {
		return ref.URL
	}
	return "issue"
}

// redactSecrets removes well-known credential shapes from free-form reports.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
}

func redactSecrets(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}
