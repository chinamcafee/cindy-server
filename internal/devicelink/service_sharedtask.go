package devicelink

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

const sharedTaskInviteTTL = 7 * 24 * time.Hour

type sharedTaskRow struct {
	ID             string
	SessionID      string
	Title          string
	OwnerAccountID string
	HostDeviceID   string
	Revision       int
	Status         string
	CreatedAt      time.Time
	ClosedAt       *time.Time
}

type sharedTaskMemberView struct {
	MemberID  string    `json:"memberId"`
	AccountID string    `json:"accountId"`
	Role      string    `json:"role"`
	Version   int       `json:"version"`
	DeviceIDs []string  `json:"deviceIds"`
	JoinedAt  time.Time `json:"joinedAt"`
}

type sharedTaskView struct {
	SharedTaskID   string                 `json:"sharedTaskId"`
	SessionID      string                 `json:"sessionId"`
	Title          string                 `json:"title"`
	OwnerAccountID string                 `json:"ownerAccountId"`
	HostDeviceID   string                 `json:"hostDeviceId"`
	Revision       int                    `json:"revision"`
	Status         string                 `json:"status"`
	CreatedAt      time.Time              `json:"createdAt"`
	ClosedAt       *time.Time             `json:"closedAt,omitempty"`
	Role           string                 `json:"role"`
	MemberID       string                 `json:"memberId,omitempty"`
	Members        []sharedTaskMemberView `json:"members,omitempty"`
	Guests         []sharedTaskGrantView  `json:"guests"`
}

type sharedTaskGrantView struct {
	MemberID  string   `json:"memberId"`
	AccountID string   `json:"accountId"`
	Version   int      `json:"version"`
	DeviceIDs []string `json:"deviceIds"`
	JoinedAt  int64    `json:"joinedAt"`
}

func (m *Module) loadSharedTask(ctx context.Context, id string) (*sharedTaskRow, error) {
	row := &sharedTaskRow{}
	err := m.db.Pool.QueryRow(ctx, `
		SELECT id::text, session_id, title, owner_account_id::text, host_device_id, revision, status, created_at, closed_at
		FROM dl_shared_tasks WHERE id = $1::uuid`, id).
		Scan(&row.ID, &row.SessionID, &row.Title, &row.OwnerAccountID, &row.HostDeviceID, &row.Revision,
			&row.Status, &row.CreatedAt, &row.ClosedAt)
	if err != nil {
		return nil, err
	}
	return row, nil
}

// sharedTaskRole returns the caller's role ("host"/"guest"), guest member id,
// host device id and task status.
func (m *Module) sharedTaskRole(ctx context.Context, taskID, account string) (role, memberID, hostDeviceID, status string, err error) {
	if !validUUID(taskID) {
		return "", "", "", "", pgx.ErrNoRows
	}
	var owner string
	err = m.db.Pool.QueryRow(ctx,
		`SELECT owner_account_id::text, host_device_id, status FROM dl_shared_tasks WHERE id = $1::uuid`, taskID).
		Scan(&owner, &hostDeviceID, &status)
	if err != nil {
		return "", "", "", "", err
	}
	if owner == account {
		return "host", account, hostDeviceID, status, nil
	}
	var guestMember string
	err = m.db.Pool.QueryRow(ctx, `
		SELECT id::text FROM dl_shared_task_guests
		WHERE shared_task_id = $1::uuid AND account_id = $2::uuid AND removed_at IS NULL`,
		taskID, account).Scan(&guestMember)
	if err == nil {
		return "guest", guestMember, hostDeviceID, status, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", hostDeviceID, status, nil
	}
	return "", "", "", "", err
}

func (m *Module) sharedTaskGuestActive(ctx context.Context, taskID, memberID string) (bool, error) {
	if !validUUID(taskID) {
		return false, nil
	}
	var exists bool
	err := m.db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM dl_shared_task_guests
			WHERE shared_task_id = $1::uuid AND member_id::text = $2 AND removed_at IS NULL
		)`, taskID, memberID).Scan(&exists)
	return exists, err
}

func (m *Module) sharedTaskAccess(ctx context.Context, taskID, account string) (bool, error) {
	role, _, _, status, err := m.sharedTaskRole(ctx, taskID, account)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return role != "" && status == "active", nil
}

func (m *Module) loadSharedTaskMembers(ctx context.Context, row *sharedTaskRow) ([]sharedTaskMemberView, error) {
	members := make([]sharedTaskMemberView, 0, 3)
	ownerDevices := []string{}
	if row.HostDeviceID != "" {
		ownerDevices = append(ownerDevices, row.HostDeviceID)
	}
	members = append(members, sharedTaskMemberView{
		MemberID:  row.OwnerAccountID,
		AccountID: row.OwnerAccountID,
		Role:      "host",
		Version:   1,
		DeviceIDs: ownerDevices,
		JoinedAt:  row.CreatedAt,
	})

	rows, err := m.db.Pool.Query(ctx, `
		SELECT id::text, account_id::text, version, device_ids, joined_at
		FROM dl_shared_task_guests
		WHERE shared_task_id = $1::uuid AND removed_at IS NULL
		ORDER BY joined_at`, row.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			member sharedTaskMemberView
			raw    []byte
		)
		if err := rows.Scan(&member.MemberID, &member.AccountID, &member.Version, &raw, &member.JoinedAt); err != nil {
			return nil, err
		}
		member.Role = "guest"
		member.DeviceIDs = []string{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &member.DeviceIDs)
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func (m *Module) buildSharedTaskView(ctx context.Context, row *sharedTaskRow, role, memberID string, withMembers bool) (*sharedTaskView, error) {
	view := &sharedTaskView{
		SharedTaskID:   row.ID,
		SessionID:      row.SessionID,
		Title:          row.Title,
		OwnerAccountID: row.OwnerAccountID,
		HostDeviceID:   row.HostDeviceID,
		Revision:       row.Revision,
		Status:         row.Status,
		CreatedAt:      row.CreatedAt,
		ClosedAt:       row.ClosedAt,
		Role:           role,
		MemberID:       memberID,
		Guests:         []sharedTaskGrantView{},
	}
	if withMembers {
		members, err := m.loadSharedTaskMembers(ctx, row)
		if err != nil {
			return nil, err
		}
		view.Members = members
		for _, member := range members {
			if member.Role == "guest" {
				view.Guests = append(view.Guests, sharedTaskGrantView{
					MemberID: member.MemberID, AccountID: member.AccountID,
					Version: member.Version, DeviceIDs: member.DeviceIDs,
					JoinedAt: member.JoinedAt.UnixMilli(),
				})
			}
		}
	}
	return view, nil
}

func taskIDParam(r *http.Request) (string, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	return id, validUUID(id)
}

func (m *Module) listSharedTasks(w http.ResponseWriter, r *http.Request) {
	account := accountID(r)
	rows, err := m.db.Pool.Query(r.Context(), `
		SELECT t.id::text, t.session_id, t.title, t.owner_account_id::text, t.host_device_id,
		       t.revision, t.status, t.created_at, t.closed_at,
		       (t.owner_account_id = $1::uuid) AS is_owner,
		       COALESCE((
		           SELECT g.id::text FROM dl_shared_task_guests g
		           WHERE g.shared_task_id = t.id AND g.account_id = $1::uuid AND g.removed_at IS NULL
		           LIMIT 1
		       ), '') AS guest_member
		FROM dl_shared_tasks t
		WHERE t.owner_account_id = $1::uuid
		   OR EXISTS (
		       SELECT 1 FROM dl_shared_task_guests g
		       WHERE g.shared_task_id = t.id AND g.account_id = $1::uuid AND g.removed_at IS NULL
		   )
		ORDER BY t.created_at DESC`, account)
	if err != nil {
		m.internalError(w, "list_shared_tasks", err)
		return
	}
	defer rows.Close()

	out := make([]sharedTaskView, 0, 8)
	for rows.Next() {
		var (
			row         sharedTaskRow
			isOwner     bool
			guestMember string
		)
		if err := rows.Scan(&row.ID, &row.SessionID, &row.Title, &row.OwnerAccountID, &row.HostDeviceID,
			&row.Revision, &row.Status, &row.CreatedAt, &row.ClosedAt, &isOwner, &guestMember); err != nil {
			m.internalError(w, "scan_shared_task", err)
			return
		}
		role, memberID := "guest", guestMember
		if isOwner {
			role, memberID = "host", account
		}
		view, err := m.buildSharedTaskView(r.Context(), &row, role, memberID, false)
		if err != nil {
			m.internalError(w, "build_shared_task", err)
			return
		}
		out = append(out, *view)
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, "list_shared_tasks", err)
		return
	}
	httpapi.OK(w, map[string]any{"sharedTasks": out})
}

func (m *Module) createSharedTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID       string   `json:"sessionId"`
		Title           string   `json:"title"`
		HostDeviceID    string   `json:"hostDeviceId"`
		GuestAccountIDs []string `json:"guestAccountIds"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		req.Title = "Shared task"
	}
	if len(req.Title) > 128 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid title"))
		return
	}
	if req.SessionID == "" || len(req.SessionID) > 200 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid sessionId"))
		return
	}
	account := accountID(r)
	host := strings.TrimSpace(req.HostDeviceID)
	if host == "" {
		host = deviceIDFromRequest(r, "")
	}
	if host == "" {
		host = "host"
	}
	if len(host) > 128 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid hostDeviceId"))
		return
	}
	if len(req.GuestAccountIDs) > 2 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "at most 2 guests"))
		return
	}
	seen := map[string]bool{account: true}
	guests := make([]string, 0, len(req.GuestAccountIDs))
	for _, g := range req.GuestAccountIDs {
		g = strings.TrimSpace(g)
		if !validUUID(g) {
			httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "invalid guest account id"))
			return
		}
		if seen[g] {
			continue
		}
		seen[g] = true
		guests = append(guests, g)
	}

	taskID := uuid.NewString()
	tx, err := m.db.Pool.Begin(r.Context())
	if err != nil {
		m.internalError(w, "create_shared_task", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	if _, err := tx.Exec(r.Context(), `
		INSERT INTO dl_shared_tasks (id, session_id, title, owner_account_id, host_device_id)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5)`, taskID, req.SessionID, req.Title, account, host); err != nil {
		m.internalError(w, "create_shared_task", err)
		return
	}
	for _, guest := range guests {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO dl_shared_task_guests (id, shared_task_id, member_id, account_id)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid)`,
			uuid.NewString(), taskID, uuid.NewString(), guest); err != nil {
			m.internalError(w, "create_shared_task_guest", err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		m.internalError(w, "create_shared_task", err)
		return
	}

	row, err := m.loadSharedTask(r.Context(), taskID)
	if err != nil {
		m.internalError(w, "load_shared_task", err)
		return
	}
	view, err := m.buildSharedTaskView(r.Context(), row, "host", account, true)
	if err != nil {
		m.internalError(w, "build_shared_task", err)
		return
	}
	m.auditEvent(r.Context(), "devicelink.shared_task.create", "shared_task", taskID, nil)
	httpapi.Created(w, view)
}

func (m *Module) getSharedTask(w http.ResponseWriter, r *http.Request) {
	id, ok := taskIDParam(r)
	if !ok {
		m.notFound(w)
		return
	}
	row, err := m.loadSharedTask(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			m.notFound(w)
			return
		}
		m.internalError(w, "get_shared_task", err)
		return
	}
	role, memberID, _, _, err := m.sharedTaskRole(r.Context(), id, accountID(r))
	if err != nil {
		m.internalError(w, "get_shared_task_role", err)
		return
	}
	if role == "" {
		m.notFound(w)
		return
	}
	view, err := m.buildSharedTaskView(r.Context(), row, role, memberID, true)
	if err != nil {
		m.internalError(w, "get_shared_task", err)
		return
	}
	httpapi.OK(w, view)
}

func (m *Module) inviteSharedTask(w http.ResponseWriter, r *http.Request) {
	id, ok := taskIDParam(r)
	if !ok {
		m.notFound(w)
		return
	}
	account := accountID(r)
	row, err := m.loadSharedTask(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			m.notFound(w)
			return
		}
		m.internalError(w, "invite_shared_task", err)
		return
	}
	if row.OwnerAccountID != account {
		httpapi.WriteError(w, httpapi.ErrForbidden)
		return
	}
	if row.Status != "active" {
		httpapi.WriteError(w, httpapi.Err(http.StatusConflict, "SHARED_TASK_CLOSED", "shared task is closed"))
		return
	}

	token, err := newInviteToken()
	if err != nil {
		m.internalError(w, "invite_shared_task", err)
		return
	}
	expiresAt := time.Now().UTC().Add(sharedTaskInviteTTL)
	if _, err := m.db.Pool.Exec(r.Context(), `
		INSERT INTO dl_shared_task_invitations (shared_task_id, token_hash, created_by, expires_at)
		VALUES ($1::uuid, $2, $3::uuid, $4)`, id, hashToken(token), account, expiresAt); err != nil {
		m.internalError(w, "invite_shared_task", err)
		return
	}
	m.auditEvent(r.Context(), "devicelink.shared_task.invite", "shared_task", id, nil)
	httpapi.OK(w, map[string]any{
		"token":        token,
		"invitation":   token,
		"sharedTaskId": id,
		"expiresAt":    expiresAt,
	})
}

func (m *Module) joinSharedTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token       string `json:"token"`
		Invitation  string `json:"invitation"`
		DisplayName string `json:"displayName"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		token = strings.TrimSpace(req.Invitation)
	}
	if len(token) != 43 {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVITE_INVALID", "invalid invite token"))
		return
	}
	account := accountID(r)
	tokenHash := hashToken(token)

	tx, err := m.db.Pool.Begin(r.Context())
	if err != nil {
		m.internalError(w, "join_shared_task", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	var (
		taskID    string
		expiresAt time.Time
		usedAt    *time.Time
	)
	err = tx.QueryRow(r.Context(), `
		SELECT shared_task_id::text, expires_at, used_at
		FROM dl_shared_task_invitations
		WHERE token_hash = $1
		FOR UPDATE`, tokenHash).Scan(&taskID, &expiresAt, &usedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVITE_INVALID", "invalid invite token"))
			return
		}
		m.internalError(w, "join_shared_task", err)
		return
	}
	if usedAt != nil || !expiresAt.After(time.Now()) {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVITE_INVALID", "invite token expired or already used"))
		return
	}

	var owner, status string
	if err := tx.QueryRow(r.Context(), `
		SELECT owner_account_id::text, status FROM dl_shared_tasks WHERE id = $1::uuid FOR UPDATE`, taskID).
		Scan(&owner, &status); err != nil {
		m.internalError(w, "join_shared_task", err)
		return
	}
	if status != "active" {
		httpapi.WriteError(w, httpapi.Err(http.StatusConflict, "SHARED_TASK_CLOSED", "shared task is closed"))
		return
	}
	if owner == account {
		httpapi.WriteError(w, httpapi.Err(http.StatusConflict, "ALREADY_MEMBER", "owner is already a member"))
		return
	}

	var existing string
	err = tx.QueryRow(r.Context(), `
		SELECT id::text FROM dl_shared_task_guests
		WHERE shared_task_id = $1::uuid AND account_id = $2::uuid AND removed_at IS NULL`,
		taskID, account).Scan(&existing)
	if err == nil {
		httpapi.OK(w, map[string]any{
			"status": "joined", "created": false, "sharedTaskId": taskID, "memberId": existing,
		})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		m.internalError(w, "join_shared_task", err)
		return
	}

	var guestCount int
	if err := tx.QueryRow(r.Context(), `
		SELECT count(*) FROM dl_shared_task_guests
		WHERE shared_task_id = $1::uuid AND removed_at IS NULL`, taskID).Scan(&guestCount); err != nil {
		m.internalError(w, "join_shared_task", err)
		return
	}
	if guestCount >= 2 {
		httpapi.WriteError(w, httpapi.Err(http.StatusConflict, "SHARED_TASK_FULL", "shared task is full"))
		return
	}

	memberID := uuid.NewString()
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO dl_shared_task_guests (id, shared_task_id, member_id, account_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid)`,
		memberID, taskID, memberID, account); err != nil {
		m.internalError(w, "join_shared_task", err)
		return
	}
	if _, err := tx.Exec(r.Context(),
		`UPDATE dl_shared_task_invitations SET used_at = now() WHERE token_hash = $1`, tokenHash); err != nil {
		m.internalError(w, "join_shared_task", err)
		return
	}
	if _, err := tx.Exec(r.Context(),
		`UPDATE dl_shared_tasks SET revision = revision + 1 WHERE id = $1::uuid`, taskID); err != nil {
		m.internalError(w, "join_shared_task", err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		m.internalError(w, "join_shared_task", err)
		return
	}

	row, err := m.loadSharedTask(r.Context(), taskID)
	if err != nil {
		m.internalError(w, "join_shared_task", err)
		return
	}
	_ = row
	httpapi.OK(w, map[string]any{
		"status": "joined", "created": true, "sharedTaskId": taskID, "memberId": memberID,
	})
}

func (m *Module) sharedTaskMembersHandler(w http.ResponseWriter, r *http.Request) {
	id, ok := taskIDParam(r)
	if !ok {
		m.notFound(w)
		return
	}
	row, err := m.loadSharedTask(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			m.notFound(w)
			return
		}
		m.internalError(w, "shared_task_members", err)
		return
	}
	role, _, _, _, err := m.sharedTaskRole(r.Context(), id, accountID(r))
	if err != nil {
		m.internalError(w, "shared_task_members", err)
		return
	}
	if role == "" {
		m.notFound(w)
		return
	}
	members, err := m.loadSharedTaskMembers(r.Context(), row)
	if err != nil {
		m.internalError(w, "shared_task_members", err)
		return
	}
	httpapi.OK(w, map[string]any{"members": members})
}

func (m *Module) removeSharedTaskMember(w http.ResponseWriter, r *http.Request) {
	id, ok := taskIDParam(r)
	if !ok {
		m.notFound(w)
		return
	}
	memberID := strings.TrimSpace(chi.URLParam(r, "memberId"))
	if memberID == "" {
		httpapi.WriteError(w, httpapi.ErrInvalidInput)
		return
	}
	account := accountID(r)
	row, err := m.loadSharedTask(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			m.notFound(w)
			return
		}
		m.internalError(w, "remove_shared_task_member", err)
		return
	}
	if row.OwnerAccountID != account {
		httpapi.WriteError(w, httpapi.ErrForbidden)
		return
	}
	if memberID == row.OwnerAccountID {
		httpapi.WriteError(w, httpapi.Err(http.StatusBadRequest, "INVALID_PARAMS", "cannot remove the owner"))
		return
	}

	tag, err := m.db.Pool.Exec(r.Context(), `
		UPDATE dl_shared_task_guests SET removed_at = now()
		WHERE shared_task_id = $1::uuid AND removed_at IS NULL
		  AND (member_id::text = $2 OR account_id::text = $2)`, id, memberID)
	if err != nil {
		m.internalError(w, "remove_shared_task_member", err)
		return
	}
	if tag.RowsAffected() == 0 {
		m.notFound(w)
		return
	}
	_, _ = m.db.Pool.Exec(r.Context(),
		`UPDATE dl_shared_tasks SET revision = revision + 1 WHERE id = $1::uuid`, id)
	httpapi.OK(w, map[string]any{"status": "removed", "memberId": memberID})
}

func (m *Module) leaveSharedTask(w http.ResponseWriter, r *http.Request) {
	id, ok := taskIDParam(r)
	if !ok {
		m.notFound(w)
		return
	}
	account := accountID(r)
	row, err := m.loadSharedTask(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			m.notFound(w)
			return
		}
		m.internalError(w, "leave_shared_task", err)
		return
	}
	if row.OwnerAccountID == account {
		httpapi.WriteError(w, httpapi.Err(http.StatusForbidden, "OWNER_CANNOT_LEAVE", "owner must close the task"))
		return
	}
	var memberID string
	if err := m.db.Pool.QueryRow(r.Context(), `
		SELECT id::text FROM dl_shared_task_guests
		WHERE shared_task_id = $1::uuid AND account_id = $2::uuid AND removed_at IS NULL`, id, account).Scan(&memberID); err != nil {
		m.notFound(w)
		return
	}
	tag, err := m.db.Pool.Exec(r.Context(), `
		UPDATE dl_shared_task_guests SET removed_at = now()
		WHERE shared_task_id = $1::uuid AND account_id = $2::uuid AND removed_at IS NULL`, id, account)
	if err != nil {
		m.internalError(w, "leave_shared_task", err)
		return
	}
	if tag.RowsAffected() == 0 {
		m.notFound(w)
		return
	}
	_, _ = m.db.Pool.Exec(r.Context(),
		`UPDATE dl_shared_tasks SET revision = revision + 1 WHERE id = $1::uuid`, id)
	httpapi.OK(w, map[string]any{"status": "left", "memberId": memberID})
}

func (m *Module) closeSharedTask(w http.ResponseWriter, r *http.Request) {
	id, ok := taskIDParam(r)
	if !ok {
		m.notFound(w)
		return
	}
	account := accountID(r)
	row, err := m.loadSharedTask(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			m.notFound(w)
			return
		}
		m.internalError(w, "close_shared_task", err)
		return
	}
	if row.OwnerAccountID != account {
		httpapi.WriteError(w, httpapi.ErrForbidden)
		return
	}
	tag, err := m.db.Pool.Exec(r.Context(), `
		UPDATE dl_shared_tasks
		SET status = 'closed', closed_at = now(), revision = revision + 1
		WHERE id = $1::uuid AND status = 'active'`, id)
	if err != nil {
		m.internalError(w, "close_shared_task", err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpapi.WriteError(w, httpapi.Err(http.StatusConflict, "SHARED_TASK_CLOSED", "shared task is already closed"))
		return
	}
	m.auditEvent(r.Context(), "devicelink.shared_task.close", "shared_task", id, nil)
	httpapi.OK(w, map[string]any{"status": "closed", "sharedTaskId": id})
}

func newInviteToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
