package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/db"
	"github.com/makecindy/cindy-server/internal/platform/objectstore"
)

type providerSeed struct {
	Slug, Name, Wire string
	Models           []modelSeed
}

type modelSeed struct {
	Key, Name          string
	Context, MaxOutput int
	Currency           string
	Input, Output      float64
	Capabilities       string
	Sort               int
}

var seeds = []providerSeed{
	{
		Slug: "openai", Name: "OpenAI", Wire: "openai-responses",
		Models: []modelSeed{
			{Key: "gpt-5.2", Name: "GPT-5.2", Context: 400000, MaxOutput: 128000, Currency: "USD", Input: 1.25, Output: 10, Capabilities: `{"agents":["chat","codex"],"modalities":{"input":["text","image"],"output":["text"]},"efforts":["low","medium","high"],"defaultEffort":"medium","supportsFastMode":true}`, Sort: 10},
			{Key: "gpt-5.2-mini", Name: "GPT-5.2 mini", Context: 200000, MaxOutput: 64000, Currency: "USD", Input: 0.25, Output: 2, Capabilities: `{"agents":["chat"],"modalities":{"input":["text","image"],"output":["text"]},"efforts":["low","medium"],"defaultEffort":"low"}`, Sort: 20},
		},
	},
	{
		Slug: "anthropic", Name: "Anthropic", Wire: "anthropic-messages",
		Models: []modelSeed{
			{Key: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6", Context: 200000, MaxOutput: 64000, Currency: "USD", Input: 3, Output: 15, Capabilities: `{"agents":["chat","claude-code"],"modalities":{"input":["text","image"],"output":["text"]},"efforts":["low","medium","high"],"defaultEffort":"medium"}`, Sort: 10},
			{Key: "claude-opus-4-6", Name: "Claude Opus 4.6", Context: 200000, MaxOutput: 32000, Currency: "USD", Input: 15, Output: 75, Capabilities: `{"agents":["chat","claude-code"],"modalities":{"input":["text","image"],"output":["text"]},"efforts":["low","medium","high"],"defaultEffort":"high"}`, Sort: 20},
		},
	},
	{
		Slug: "google", Name: "Google", Wire: "google-generative",
		Models: []modelSeed{
			{Key: "gemini-3-pro", Name: "Gemini 3 Pro", Context: 1048576, MaxOutput: 65536, Currency: "USD", Input: 2, Output: 12, Capabilities: `{"agents":["chat"],"modalities":{"input":["text","image","audio"],"output":["text"]},"efforts":["low","medium","high"],"defaultEffort":"medium"}`, Sort: 10},
		},
	},
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("db", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	if err := db.Migrate(ctx, database); err != nil {
		slog.Error("migrate", "error", err)
		os.Exit(1)
	}

	for _, p := range seeds {
		var providerID string
		err := database.Pool.QueryRow(ctx, `
			INSERT INTO ma_providers (slug, name, region, wire_protocol, metadata)
			VALUES ($1,$2,$3,$4,'{}'::jsonb)
			ON CONFLICT (slug) DO UPDATE SET name=EXCLUDED.name, wire_protocol=EXCLUDED.wire_protocol
			RETURNING id::text`, p.Slug, p.Name, cfg.Region, p.Wire).Scan(&providerID)
		if err != nil {
			slog.Error("seed_provider", "provider", p.Slug, "error", err)
			os.Exit(1)
		}
		for _, m := range p.Models {
			pricing := map[string]any{"inputPerMillion": m.Input, "outputPerMillion": m.Output, "currency": m.Currency}
			_, err := database.Pool.Exec(ctx, `
				INSERT INTO ma_models (provider_id, model_key, display_name, capabilities, context_window, max_output_tokens, pricing, currency, status, sort)
				VALUES ($1::uuid,$2,$3,$4::jsonb,$5,$6,$7::jsonb,$8,'active',$9)
				ON CONFLICT (provider_id, model_key) DO UPDATE SET display_name=EXCLUDED.display_name, capabilities=EXCLUDED.capabilities,
					context_window=EXCLUDED.context_window, max_output_tokens=EXCLUDED.max_output_tokens, pricing=EXCLUDED.pricing, updated_at=now()`,
				providerID, m.Key, m.Name, m.Capabilities, m.Context, m.MaxOutput, mustJSON(pricing), m.Currency, m.Sort)
			if err != nil {
				slog.Error("seed_model", "model", m.Key, "error", err)
				os.Exit(1)
			}
		}
	}

	plans := []struct {
		Slug, Name string
		Price      int64
		Currency   string
	}{
		{"free", "Free", 0, "USD"},
		{"pro", "Pro", 2000, "USD"},
		{"team", "Team", 4000, "USD"},
	}
	for _, plan := range plans {
		_, err := database.Pool.Exec(ctx, `
			INSERT INTO ma_plans (slug, name, price_minor, currency, interval, features)
			VALUES ($1,$2,$3,$4,'month','{"credits":"included"}'::jsonb)
			ON CONFLICT (slug) DO UPDATE SET name=EXCLUDED.name, price_minor=EXCLUDED.price_minor, currency=EXCLUDED.currency`,
			plan.Slug, plan.Name, plan.Price, plan.Currency)
		if err != nil {
			slog.Error("seed_plan", "plan", plan.Slug, "error", err)
			os.Exit(1)
		}
	}

	flags := []struct{ Key, Description, Value string }{
		{"isCanary", "Enable canary updates for this account", "false"},
		{"defaultEnableBeta", "Enable beta updates by default", "false"},
		{"cloudCompanions", "Cloud companion runtime", "true"},
		{"botGroupChat", "Bot group chat", "true"},
	}
	for _, f := range flags {
		_, err := database.Pool.Exec(ctx, `
			INSERT INTO feature_flags (key, default_value, description)
			VALUES ($1,$2::jsonb,$3)
			ON CONFLICT (key) DO NOTHING`, f.Key, f.Value, f.Description)
		if err != nil {
			slog.Error("seed_flag", "flag", f.Key, "error", err)
			os.Exit(1)
		}
	}

	ann := []struct {
		Platform, Version, Locale, Title, Body string
	}{
		{"desktop", "1.0.0", "zh-CN", "欢迎使用 Cindy 重建服务端", "本地开发环境已就绪，可在管理后台查看服务状态。"},
		{"mobile", "1.0.0", "en", "Welcome to Cindy", "The local development server is ready."},
	}
	for _, a := range ann {
		_, err := database.Pool.Exec(ctx, `
			INSERT INTO announcements (platform, version, locale, title, body, status, published_at)
			SELECT $1,$2,$3,$4,$5,'published',now()
			WHERE NOT EXISTS (SELECT 1 FROM announcements WHERE platform=$1 AND version=$2 AND locale=$3)`,
			a.Platform, a.Version, a.Locale, a.Title, a.Body)
		if err != nil {
			slog.Error("seed_announcement", "error", err)
			os.Exit(1)
		}
	}

	seedDemoWorkspace(ctx, database)
	seedObjects(ctx, cfg, database)

	slog.Info("seed_complete", "providers", len(seeds), "plans", len(plans), "flags", len(flags))
}

const (
	demoAccountID    = "11111111-1111-4111-8111-111111111111"
	demoMembershipID = "22222222-2222-4222-8222-222222222222"
	demoOrgID        = "33333333-3333-4333-8333-333333333333"
	demoOrgMemberID  = "44444444-4444-4444-8444-444444444444"
	demoPluginID     = "55555555-5555-4555-8555-555555555555"
	demoReleaseID    = "66666666-6666-4666-8666-666666666666"
	demoSkillID      = "77777777-7777-4777-8777-777777777777"
	demoSkillVerID   = "88888888-8888-4888-8888-888888888888"
	demoSharedTaskID = "99999999-9999-4999-8999-999999999999"
	demoShareID      = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

func seedDemoWorkspace(ctx context.Context, database *db.DB) {
	email := "demo.user@cindy.local"
	_, err := database.Pool.Exec(ctx, `INSERT INTO auth_accounts (id, region) VALUES ($1::uuid,'global') ON CONFLICT (id) DO NOTHING`, demoAccountID)
	if err != nil {
		slog.Warn("seed_demo_account", "error", err)
		return
	}
	_, err = database.Pool.Exec(ctx, `INSERT INTO auth_identities (account_id, type, identifier, verified_at)
		VALUES ($1::uuid,'email',$2,now()) ON CONFLICT (type, identifier) DO NOTHING`, demoAccountID, email)
	if err != nil {
		slog.Warn("seed_demo_identity", "error", err)
	}
	_, _ = database.Pool.Exec(ctx, `INSERT INTO auth_memberships (id, account_id, kind, role, display_name, email, avatar_url)
		VALUES ($1::uuid,$2::uuid,'personal','owner','Demo User',$3,NULL) ON CONFLICT (id) DO NOTHING`, demoMembershipID, demoAccountID, email)
	_, _ = database.Pool.Exec(ctx, `INSERT INTO auth_organizations (id, slug, name, region, plan, sso_required, plugin_prefix)
		VALUES ($1::uuid,'cindy-labs','Cindy Labs','global','team',false,'cindylabs') ON CONFLICT (id) DO NOTHING`, demoOrgID)
	_, _ = database.Pool.Exec(ctx, `INSERT INTO auth_memberships (id, account_id, kind, role, display_name, email, org_id, org_name)
		VALUES ($1::uuid,$2::uuid,'org','owner','Demo User',$3,$4::uuid,'Cindy Labs') ON CONFLICT (id) DO NOTHING`, demoOrgMemberID, demoAccountID, email, demoOrgID)
	_, _ = database.Pool.Exec(ctx, `INSERT INTO auth_org_domains (org_id, domain, verified_at) VALUES ($1::uuid,'cindylabs.example',now()) ON CONFLICT (domain) DO NOTHING`, demoOrgID)

	devices := []struct {
		ID, Name, Platform, Version string
		Remote                      bool
	}{
		{"demo-macbook", "Demo MacBook", "darwin", "1.4.2", true},
		{"demo-iphone", "Demo iPhone", "ios", "1.4.0", false},
	}
	for _, d := range devices {
		_, _ = database.Pool.Exec(ctx, `INSERT INTO dl_devices (id, account_id, name, platform, app_version, remote_control_enabled, last_seen_at, device_info)
			VALUES ($1,$2::uuid,$3,$4,$5,$6,now(),'{"cpuLabel":"Apple M4","memoryGb":32}'::jsonb)
			ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, app_version=EXCLUDED.app_version, last_seen_at=now()`,
			d.ID, demoAccountID, d.Name, d.Platform, d.Version, d.Remote)
	}

	manifest := `{"schemaVersion":3,"minCindyVersion":"1.4.0","id":"hello-world","name":"Hello World","version":"1.0.0","description":"A demo plugin for the rebuilt server.","entry":"index.html","kind":"chip"}`
	_, _ = database.Pool.Exec(ctx, `INSERT INTO plugin_plugins (id, ghost_id, scope, name, description, author, status, default_install, namespace)
		VALUES ($1::uuid,'hello-world','public','Hello World','A demo plugin for the rebuilt server.','Cindy Labs','active',true,NULL)
		ON CONFLICT (ghost_id, scope, org_id) DO NOTHING`, demoPluginID)
	_, _ = database.Pool.Exec(ctx, `INSERT INTO plugin_releases (id, plugin_id, version, manifest, sha256, size_bytes, review_status, status, published_at)
		VALUES ($1::uuid,$2::uuid,'1.0.0',$3::jsonb,repeat('a',64),10240,'approved','active',now())
		ON CONFLICT (plugin_id, version) DO NOTHING`, demoReleaseID, demoPluginID, manifest)
	_, _ = database.Pool.Exec(ctx, `INSERT INTO plugin_usage_daily (plugin_id, release_id, day, event_count) VALUES ($1::uuid,$2::uuid,current_date,128)
		ON CONFLICT (plugin_id, release_id, day) DO UPDATE SET event_count=EXCLUDED.event_count`, demoPluginID, demoReleaseID)

	_, _ = database.Pool.Exec(ctx, `INSERT INTO sk_skills (id, slug, owner_account_id, owner_type, display_name, summary, description, visibility, status, latest_version, github_url)
		VALUES ($1::uuid,'repo-review',$2::uuid,'organization','Repository Review','Review a repository and produce findings.','A demo skill used by the rebuilt control plane.','public','active','1.0.0','https://github.com/makecindy/cindy')
		ON CONFLICT (slug) DO NOTHING`, demoSkillID, demoAccountID)
	_, _ = database.Pool.Exec(ctx, `INSERT INTO sk_versions (id, skill_id, version, folder_hash, zip_sha256, file_size, status, scan_status)
		VALUES ($1::uuid,$2::uuid,'1.0.0',repeat('b',64),repeat('c',64),20480,'published','published')
		ON CONFLICT (skill_id, version) DO NOTHING`, demoSkillVerID, demoSkillID)
	_, _ = database.Pool.Exec(ctx, `INSERT INTO sk_auto_install (org_id, skill_id, version, audience)
		VALUES ($1::uuid,$2::uuid,'1.0.0','{"teams":["engineering"]}'::jsonb) ON CONFLICT (org_id, skill_id) DO NOTHING`, demoOrgID, demoSkillID)

	desktopManifest := `{"app":{"version":"1.4.2","releaseNotes":"Demo release","hotfix":{"file":"Cindy-1.4.2.zip","sha256":"` + repeat("d", 64) + `","size":104857600},"requireRelogin":false}}`
	_, _ = database.Pool.Exec(ctx, `INSERT INTO upd_desktop_releases (platform, arch, channel, version, manifest, status, published_at)
		VALUES ('darwin','arm64','release','1.4.2',$1::jsonb,'published',now()) ON CONFLICT (platform, arch, channel, version) DO NOTHING`, desktopManifest)
	_, _ = database.Pool.Exec(ctx, `INSERT INTO upd_mobile_releases (platform, channel, version, build_number, runtime_version, install_url, min_version, release_notes, status, published_at)
		VALUES ('ios','release','1.4.0','140','1.4.0','https://example.com/cindy.ipa','1.2.0','Demo mobile release','published',now())
		ON CONFLICT (platform, channel, version) DO NOTHING`)

	_, _ = database.Pool.Exec(ctx, `INSERT INTO dl_shared_tasks (id, session_id, owner_account_id, host_device_id, status)
		VALUES ($1::uuid,'demo-session-1',$2::uuid,'demo-macbook','active') ON CONFLICT (id) DO NOTHING`, demoSharedTaskID, demoAccountID)
	_, _ = database.Pool.Exec(ctx, `INSERT INTO dl_provider_shares (id, owner_account_id, provider_id, provider_label, host_device_id, status)
		VALUES ($1::uuid,$2::uuid,'anthropic','Claude on Demo MacBook','demo-macbook','active') ON CONFLICT (id) DO NOTHING`, demoShareID, demoAccountID)
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func seedObjects(ctx context.Context, cfg *config.Config, database *db.DB) {
	store, err := objectstore.New(ctx, cfg)
	if err != nil {
		slog.Warn("seed_objects_storage_unavailable", "error", err)
		return
	}
	pluginZip, err := zipArchive(map[string]string{
		"ghost.json": `{"schemaVersion":3,"minCindyVersion":"1.4.0","id":"hello-world","name":"Hello World","version":"1.0.0","description":"A demo plugin for the rebuilt server.","entry":"index.html","kind":"chip"}`,
		"index.html": "<!doctype html><html><body><h1>Hello from Cindy</h1></body></html>",
	})
	if err != nil {
		slog.Warn("seed_plugin_zip", "error", err)
		return
	}
	pluginSum := sha256.Sum256(pluginZip)
	pluginSHA := hex.EncodeToString(pluginSum[:])
	_, _ = database.Pool.Exec(ctx, `UPDATE plugin_releases SET sha256=$1, size_bytes=$2 WHERE id=$3::uuid`, pluginSHA, len(pluginZip), demoReleaseID)
	pluginKey := "plugins/" + demoPluginID + "/" + demoReleaseID + ".cindy"
	if _, err := store.Client().PutObject(ctx, store.Plugins, pluginKey, bytes.NewReader(pluginZip), int64(len(pluginZip)), minio.PutObjectOptions{ContentType: "application/octet-stream"}); err != nil {
		slog.Warn("seed_plugin_object", "error", err)
	}

	skillZip, err := zipArchive(map[string]string{
		"SKILL.md": "---\nname: repo-review\ndescription: Review a repository and produce findings.\n---\n\n# Repository Review\n\n1. Read the diff.\n2. Report findings.\n",
	})
	if err != nil {
		slog.Warn("seed_skill_zip", "error", err)
		return
	}
	skillSum := sha256.Sum256(skillZip)
	skillSHA := hex.EncodeToString(skillSum[:])
	_, _ = database.Pool.Exec(ctx, `UPDATE sk_versions SET zip_sha256=$1, file_size=$2, folder_hash=$1 WHERE id=$3::uuid`, skillSHA, len(skillZip), demoSkillVerID)
	_, _ = database.Pool.Exec(ctx, `DELETE FROM sk_files WHERE version_id=$1::uuid`, demoSkillVerID)
	_, _ = database.Pool.Exec(ctx, `INSERT INTO sk_files (version_id, path, size_bytes, language, sha256, truncated) VALUES ($1::uuid,'SKILL.md',$2,'markdown',$3,false)`, demoSkillVerID, len(skillZip), skillSHA)
	skillKey := "skills/repo-review/1.0.0/" + skillSHA + ".zip"
	if _, err := store.Client().PutObject(ctx, store.Skills, skillKey, bytes.NewReader(skillZip), int64(len(skillZip)), minio.PutObjectOptions{ContentType: "application/zip"}); err != nil {
		slog.Warn("seed_skill_object", "error", err)
	}
}

func zipArchive(files map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(content)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
