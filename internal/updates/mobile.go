package updates

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const (
	channelRelease = "release"
	channelBeta    = "beta"
	channelCanary  = "canary"
)

type mobileRelease struct {
	ID             string
	Version        string
	BuildNumber    string
	RuntimeVersion string
	InstallURL     string
	ItmsURL        string
	MinVersion     string
	ReleaseNotes   string
	PublishedAt    time.Time
}

type mobileLatestResponse struct {
	Version        string `json:"version"`
	BuildNumber    string `json:"buildNumber,omitempty"`
	RuntimeVersion string `json:"runtimeVersion"`
	InstallURL     string `json:"installUrl,omitempty"`
	ItmsURL        string `json:"itmsUrl,omitempty"`
	ReleaseNotes   string `json:"releaseNotes,omitempty"`
	MinVersion     string `json:"minVersion,omitempty"`
}

// mobileLatest is the mutable per-platform/channel pointer consumed by the
// self-hosted mobile build. No record means "no update", never an error state.
func (m *Module) mobileLatest(w http.ResponseWriter, r *http.Request) {
	setNoCache(w)

	platform, err := normalizePlatform(r.URL.Query().Get("platform"))
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	channel, err := normalizeChannel(r.URL.Query().Get("channel"))
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	release, err := m.latestRelease(r, platform, channel)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}

	httpapi.OK(w, mobileLatestResponse{
		Version:        release.Version,
		BuildNumber:    release.BuildNumber,
		RuntimeVersion: release.RuntimeVersion,
		InstallURL:     release.InstallURL,
		ItmsURL:        release.ItmsURL,
		ReleaseNotes:   release.ReleaseNotes,
		MinVersion:     release.MinVersion,
	})
}

// expoManifest is the minimal Expo updates protocol manifest consumed by
// expo-updates. Only the fields required by the native parser are emitted.
type expoManifest struct {
	ID             string          `json:"id"`
	CreatedAt      string          `json:"createdAt"`
	RuntimeVersion string          `json:"runtimeVersion"`
	LaunchAsset    expoLaunchAsset `json:"launchAsset"`
}

type expoLaunchAsset struct {
	URL         string `json:"url"`
	ContentType string `json:"contentType"`
}

// mobileManifest serves the self-hosted OTA manifest. runtimeVersion is opaque
// and matched exactly; a mismatch means there is no compatible update.
func (m *Module) mobileManifest(w http.ResponseWriter, r *http.Request) {
	setNoCache(w)

	q := r.URL.Query()
	platform, err := normalizePlatformOptional(firstNonEmpty(r.Header.Get("expo-platform"), q.Get("platform")))
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	channel, err := normalizeChannel(firstNonEmpty(r.Header.Get("x-cindy-update-channel"), q.Get("channel")))
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	runtimeVersion := strings.TrimSpace(firstNonEmpty(r.Header.Get("expo-runtime-version"), q.Get("runtimeVersion")))

	release, err := m.latestRelease(r, platform, channel)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	// The native client always sends expo-runtime-version; when present it
	// must match exactly. An absent header is tolerated for local probes.
	if runtimeVersion != "" && release.RuntimeVersion != runtimeVersion {
		writeNoRelease(w)
		return
	}

	httpapi.OK(w, expoManifest{
		ID:             release.ID,
		CreatedAt:      release.PublishedAt.UTC().Format(time.RFC3339),
		RuntimeVersion: release.RuntimeVersion,
		LaunchAsset: expoLaunchAsset{
			// Until a JS bundle CDN is wired, the published install URL is the
			// only stable launch target available in upd_mobile_releases.
			URL:         firstNonEmpty(release.InstallURL, release.ItmsURL),
			ContentType: "application/javascript",
		},
	})
}

// latestRelease loads and validates the newest published release. Releases
// with invalid version/runtime/link fields are treated as "no release" so the
// client never consumes a half-valid pointer.
func (m *Module) latestRelease(r *http.Request, platform, channel string) (mobileRelease, error) {
	if m.deps.DB == nil {
		return mobileRelease{}, errNoRelease()
	}
	var rel mobileRelease
	err := m.deps.DB.Pool.QueryRow(r.Context(), `
		SELECT id::text, version, build_number, runtime_version,
		       COALESCE(install_url, ''), COALESCE(itms_url, ''),
		       COALESCE(min_version, ''), COALESCE(release_notes, ''),
		       COALESCE(published_at, created_at)
		FROM upd_mobile_releases
		WHERE ($1 = '' OR platform = $1) AND channel = $2 AND status = 'published'
		ORDER BY COALESCE(published_at, created_at) DESC, created_at DESC
		LIMIT 1`, platform, channel).Scan(
		&rel.ID, &rel.Version, &rel.BuildNumber, &rel.RuntimeVersion,
		&rel.InstallURL, &rel.ItmsURL, &rel.MinVersion, &rel.ReleaseNotes, &rel.PublishedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return mobileRelease{}, errNoRelease()
		}
		m.log.Error("mobile_release_lookup_failed", "platform", platform, "channel", channel, "error", err)
		return mobileRelease{}, httpapi.ErrInternal
	}
	if !validReleaseFields(rel) {
		return mobileRelease{}, errNoRelease()
	}
	return rel, nil
}

func validReleaseFields(rel mobileRelease) bool {
	if !isDottedVersion(rel.Version) {
		return false
	}
	if strings.TrimSpace(rel.RuntimeVersion) == "" {
		return false
	}
	if strings.TrimSpace(rel.InstallURL) == "" && strings.TrimSpace(rel.ItmsURL) == "" {
		return false
	}
	if rel.MinVersion != "" && !isDottedVersion(rel.MinVersion) {
		return false
	}
	return true
}

// isDottedVersion accepts dot-separated decimal segments that fit a safe
// integer, matching the mobile client's version validator.
func isDottedVersion(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 64 {
		return false
	}
	for _, part := range strings.Split(v, ".") {
		if part == "" || len(part) > 15 {
			return false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
		if _, err := strconv.ParseInt(part, 10, 64); err != nil {
			return false
		}
	}
	return true
}

func normalizePlatform(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ios":
		return "ios", nil
	case "android":
		return "android", nil
	default:
		return "", httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "platform must be ios or android")
	}
}

// normalizePlatformOptional accepts an empty platform for plain probes.
func normalizePlatformOptional(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	return normalizePlatform(raw)
}

func normalizeChannel(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", channelRelease:
		return channelRelease, nil
	case channelBeta:
		return channelBeta, nil
	case channelCanary:
		return channelCanary, nil
	default:
		return "", httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "channel must be release, beta or canary")
	}
}

func errNoRelease() error {
	return httpapi.Err(http.StatusNotFound, "NO_RELEASE", "no release published")
}

func writeNoRelease(w http.ResponseWriter) {
	setNoCache(w)
	httpapi.WriteError(w, errNoRelease())
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
