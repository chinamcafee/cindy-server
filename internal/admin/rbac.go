package admin

import (
	"net/http"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

// permission is a coarse-grained write capability. Reads do not require a
// permission: every authenticated admin (including readonly) may issue GETs.
type permission string

const (
	permAccountsWrite permission = "accounts.write"
	permBansWrite     permission = "bans.write"
	permOrgsWrite     permission = "orgs.write"
	permDevicesWrite  permission = "devices.write"
	permModelsWrite   permission = "models.write"
	permPluginsReview permission = "plugins.review"
	permSkillsReview  permission = "skills.review"
	permUpdatesWrite  permission = "updates.write"
	permConfigWrite   permission = "config.write"
	permAdminsWrite   permission = "admins.write"
	permOrdersWrite   permission = "orders.write"
)

// adminRoles lists the roles accepted on admin account creation.
var adminRoles = []string{
	"super_admin", "ops", "support", "model_ops", "plugin_reviewer",
	"skill_reviewer", "finance", "security", "readonly",
}

var validRole = func() map[string]bool {
	m := make(map[string]bool, len(adminRoles))
	for _, r := range adminRoles {
		m[r] = true
	}
	return m
}()

// rolePermissions is the static role -> write permission matrix.
var rolePermissions = map[string]map[permission]bool{
	"ops": {
		permAccountsWrite: true,
		permBansWrite:     true,
		permOrgsWrite:     true,
		permDevicesWrite:  true,
		permUpdatesWrite:  true,
	},
	"model_ops": {
		permModelsWrite: true,
	},
	"plugin_reviewer": {
		permPluginsReview: true,
	},
	"skill_reviewer": {
		permSkillsReview: true,
	},
	"security": {
		permBansWrite: true,
	},
	"finance": {
		permOrdersWrite: true,
	},
	"readonly": {},
}

func roleAllowed(role string, p permission) bool {
	if role == "super_admin" {
		return true
	}
	perms, ok := rolePermissions[role]
	if !ok {
		return false
	}
	return perms[p]
}

// require guards a write handler with a permission check.
func (m *Module) require(p permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := adminFrom(r.Context())
		if a.AdminID == "" {
			httpapi.WriteError(w, httpapi.ErrUnauthorized)
			return
		}
		if !roleAllowed(a.Role, p) {
			httpapi.WriteError(w, httpapi.ErrWithDetails(
				http.StatusForbidden, "FORBIDDEN", "insufficient permissions",
				map[string]any{"requiredPermission": string(p)},
			))
			return
		}
		next(w, r)
	}
}
