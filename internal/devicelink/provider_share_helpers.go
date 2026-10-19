package devicelink

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (m *Module) region() string {
	if m.cfg != nil && (m.cfg.Region == "cn" || m.cfg.Region == "global") {
		return m.cfg.Region
	}
	return "global"
}

// accountPerson resolves the public account projection used by provider-share
// owner/member/request views. Missing profiles get a stable display name.
func (m *Module) accountPerson(ctx context.Context, accountID string) (string, *string, string) {
	displayName := "Cindy user"
	var avatar string
	err := m.db.Pool.QueryRow(ctx, `
		SELECT COALESCE(display_name,''), COALESCE(avatar_url,'')
		FROM auth_memberships
		WHERE account_id = $1::uuid AND kind = 'personal'
		ORDER BY created_at LIMIT 1`, accountID).Scan(&displayName, &avatar)
	if err != nil && err != pgx.ErrNoRows {
		return "Cindy user", nil, m.region()
	}
	if displayName == "" {
		displayName = "Cindy user"
	}
	var avatarURL *string
	if avatar != "" {
		avatarURL = &avatar
	}
	return displayName, avatarURL, m.region()
}
