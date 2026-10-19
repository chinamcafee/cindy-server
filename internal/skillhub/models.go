package skillhub

// SkillInfo wire model. Optional fields are omitted rather than emitted as
// zero values where the desktop projection distinguishes undefined from empty.

type ownerRef struct {
	Type string `json:"type"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type publisherRef struct {
	Name string `json:"name"`
}

type taxonomyRef struct {
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Source string `json:"source,omitempty"`
}

type pendingVersionRef struct {
	Version string `json:"version"`
	Status  string `json:"status"`
}

type skillStats struct {
	Downloads int64 `json:"downloads"`
}

type skillItem struct {
	Slug             string             `json:"slug"`
	Icon             *string            `json:"icon"`
	DisplayName      string             `json:"displayName"`
	Summary          *string            `json:"summary"`
	Description      *string            `json:"description"`
	Version          string             `json:"version"`
	MarketVersion    string             `json:"marketVersion,omitempty"`
	PendingVersion   *pendingVersionRef `json:"pendingVersion,omitempty"`
	FolderHash       string             `json:"folderHash,omitempty"`
	FileHash         string             `json:"fileHash,omitempty"`
	Owner            ownerRef           `json:"owner"`
	Publisher        publisherRef       `json:"publisher"`
	Visibility       string             `json:"visibility"`
	ModerationStatus string             `json:"moderationStatus"`
	UpdatedAt        string             `json:"updatedAt"`
	IsMine           bool               `json:"isMine"`
	IsCreator        bool               `json:"isCreator"`
	CanManage        bool               `json:"canManage"`
	Categories       []taxonomyRef      `json:"categories"`
	Tags             []taxonomyRef      `json:"tags"`
	GitHubURL        *string            `json:"githubUrl"`
	Stats            skillStats         `json:"stats"`
}

type skillsListResponse struct {
	Items []skillItem `json:"items"`
	Total int64       `json:"total"`
}

type installedSkillsResponse struct {
	Items          []skillItem `json:"items"`
	Total          int64       `json:"total"`
	AvailableCount int64       `json:"availableCount"`
}

type skillRefInput struct {
	Slug    string `json:"slug"`
	Version string `json:"version"`
}

type installedSkillsBody struct {
	InstalledSkills []skillRefInput `json:"installedSkills"`
}

type batchDetailBody struct {
	Slugs []string `json:"slugs"`
	Scope string   `json:"scope"`
}

type fileEntry struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Language  string `json:"language"`
	Truncated bool   `json:"truncated"`
	SHA256    string `json:"sha256,omitempty"`
}

type filesResponse struct {
	Slug    string      `json:"slug"`
	Version string      `json:"version"`
	Files   []fileEntry `json:"files"`
}

type fileContentResponse struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Language  string `json:"language"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
}

type versionItem struct {
	Version   string `json:"version"`
	CreatedAt string `json:"createdAt"`
	Status    string `json:"status"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Downloads int64  `json:"downloads"`
}

type scanResponse struct {
	Status          string         `json:"status"`
	RejectionReason string         `json:"rejectionReason,omitempty"`
	Gates           []any          `json:"gates"`
	Scorecard       map[string]any `json:"scorecard"`
}

type downloadSkillResponse struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
	FileHash  string `json:"fileHash"`
	FileSize  int64  `json:"fileSize"`
	ZipSHA256 string `json:"zipSha256"`
}

type publishInitResponse struct {
	NextVersion string `json:"nextVersion"`
	OSSKey      string `json:"ossKey"`
	UploadURL   string `json:"uploadUrl"`
	ExpiresAt   string `json:"expiresAt"`
}

type publishCommitBody struct {
	OSSKey       string   `json:"ossKey"`
	Slug         string   `json:"slug"`
	Version      string   `json:"version"`
	Changelog    string   `json:"changelog"`
	DisplayName  string   `json:"displayName"`
	Summary      string   `json:"summary"`
	Description  string   `json:"description"`
	Tags         []string `json:"tags"`
	Visibility   string   `json:"visibility"`
	DeptTeamSlug string   `json:"deptTeamSlug"`
	TeamSlug     string   `json:"teamSlug"`
	VisibleSlugs []string `json:"visibleSlugs"`
}

type publishCommitResponse struct {
	Status  string `json:"status"`
	SkillID string `json:"skillId"`
	Version string `json:"version"`
}

type teamRef struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Source     string `json:"source,omitempty"`
	IsPersonal bool   `json:"isPersonal"`
	MyRole     string `json:"myRole,omitempty"`
}

type departmentRef struct {
	DeptID string `json:"deptId"`
	Name   string `json:"name"`
	Path   string `json:"path,omitempty"`
}

type departmentsResponse struct {
	Departments     []departmentRef `json:"departments"`
	FirstLevelDepts []departmentRef `json:"firstLevelDepts"`
	AllDeptIDs      []string        `json:"allDeptIds"`
}

type categoryNode struct {
	Slug         string         `json:"slug"`
	Name         string         `json:"name"`
	Source       string         `json:"source,omitempty"`
	SkillCount   int64          `json:"skillCount"`
	MySkillCount int64          `json:"mySkillCount"`
	Children     []categoryNode `json:"children"`
}

type sharedTeamRef struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type visibilityResponse struct {
	SharedTeams  []sharedTeamRef `json:"sharedTeams"`
	VisibleDepts []string        `json:"visibleDepts"`
}

type autoInstallSkill struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	CatalogScope string `json:"catalogScope"`
}

type autoInstallResponse struct {
	Skills     []autoInstallSkill `json:"skills"`
	NextCursor *string            `json:"nextCursor"`
}
