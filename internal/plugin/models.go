package plugin

import "encoding/json"

// Plugin market wire models. Field names mirror the client v2 parser exactly.

type releaseSummary struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"sizeBytes"`
	PublishedAt string `json:"publishedAt"`
	Icon        any    `json:"icon"`
}

type pluginSummary struct {
	ID             string         `json:"id"`
	GhostID        string         `json:"ghostId"`
	Name           string         `json:"name"`
	Description    *string        `json:"description"`
	Author         *string        `json:"author"`
	Scope          string         `json:"scope"`
	Namespace      *string        `json:"namespace,omitempty"`
	OrganizationID *string        `json:"organizationId"`
	DefaultInstall bool           `json:"defaultInstall"`
	CurrentRelease releaseSummary `json:"currentRelease"`
}

type releaseDetail struct {
	releaseSummary
	Manifest json.RawMessage `json:"manifest"`
}

// pluginDetail shadows the embedded summary's currentRelease so the manifest
// can be included without duplicating the base fields.
type pluginDetail struct {
	pluginSummary
	CurrentRelease releaseDetail `json:"currentRelease"`
}

type removalNotice struct {
	PluginID       string  `json:"pluginId"`
	GhostID        string  `json:"ghostId"`
	Scope          string  `json:"scope"`
	Namespace      *string `json:"namespace,omitempty"`
	OrganizationID *string `json:"organizationId"`
	Action         string  `json:"action"`
	RemovedAt      string  `json:"removedAt"`
}

type listPluginsResponse struct {
	SchemaVersion       int             `json:"schemaVersion"`
	Plugins             []pluginSummary `json:"plugins"`
	NextCursor          *string         `json:"nextCursor"`
	Removals            []removalNotice `json:"removals"`
	CurrentOrganization map[string]any  `json:"currentOrganization"`
}

type getPluginResponse struct {
	SchemaVersion int          `json:"schemaVersion"`
	Plugin        pluginDetail `json:"plugin"`
}

type downloadResponse struct {
	PluginID  string  `json:"pluginId"`
	ReleaseID string  `json:"releaseId"`
	GhostID   string  `json:"ghostId"`
	Namespace *string `json:"namespace"`
	URL       string  `json:"url"`
	ExpiresAt string  `json:"expiresAt"`
	SHA256    string  `json:"sha256"`
	SizeBytes int64   `json:"sizeBytes"`
}

// Publisher wire models.

type prepareUploadResponse struct {
	UploadID  string            `json:"uploadId"`
	PutURL    string            `json:"putUrl"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt string            `json:"expiresAt"`
	Status    string            `json:"status"`
}

type uploadFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type uploadStatusResponse struct {
	UploadID     string         `json:"uploadId"`
	Status       string         `json:"status"`
	PluginID     *string        `json:"pluginId"`
	ReleaseID    *string        `json:"releaseId"`
	GhostID      *string        `json:"ghostId"`
	Version      *string        `json:"version"`
	ReviewStatus *string        `json:"reviewStatus"`
	Failure      *uploadFailure `json:"failure"`
}

type myReleaseItem struct {
	uploadStatusResponse
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type myReleasesResponse struct {
	Releases   []myReleaseItem `json:"releases"`
	NextCursor *string         `json:"nextCursor"`
}

type commitUploadResponse struct {
	UploadID string         `json:"uploadId"`
	Status   string         `json:"status"`
	Failure  *uploadFailure `json:"failure,omitempty"`
}

// Usage wire models.

type usageEventInput struct {
	EventID    string `json:"eventId"`
	ReleaseID  string `json:"releaseId"`
	OccurredAt string `json:"occurredAt"`
}

type usageRejected struct {
	EventID string `json:"eventId"`
	Code    string `json:"code"`
}

type usageAck struct {
	Accepted  []string        `json:"accepted"`
	Duplicate []string        `json:"duplicate"`
	Rejected  []usageRejected `json:"rejected"`
}
