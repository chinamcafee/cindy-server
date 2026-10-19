package chat

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/makecindy/cindy-server/internal/platform/audit"
	"github.com/makecindy/cindy-server/internal/platform/authn"
	"github.com/makecindy/cindy-server/internal/platform/config"
	"github.com/makecindy/cindy-server/internal/platform/db"
	"github.com/makecindy/cindy-server/internal/platform/httpapi"
	"github.com/makecindy/cindy-server/internal/platform/idempotency"
	"github.com/makecindy/cindy-server/internal/platform/objectstore"
)

type Deps struct {
	DB     *db.DB
	Redis  *redis.Client
	Tokens *authn.Manager
	Idem   *idempotency.Store
	Audit  *audit.Logger
	S3     *objectstore.Store
	Config *config.Config
}

type Module struct {
	deps   Deps
	hub    *hub
	logger *slog.Logger
}

func New(d Deps) *Module {
	return &Module{deps: d, hub: newHub(), logger: slog.Default()}
}

func (m *Module) Routes() http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Get("/ready", m.handleReady)
		r.Get("/ws", m.handleWS)
		r.Group(func(r chi.Router) {
			r.Use(httpapi.Auth(m.deps.Tokens))

			r.Get("/me", m.handleMe)
			r.Post("/profile/refresh", m.handleProfileRefresh)

			r.Get("/actors", m.handleListActors)
			r.Post("/actors", m.handleCreateActor)
			r.Patch("/actors/{actorId}", m.handlePatchActor)
			r.Post("/actors/{actorId}/profile", m.handleActorProfile)

			r.Get("/preferences", m.handleListPreferences)
			r.Post("/preferences", m.handleWritePreference)

			r.Get("/conversations", m.handleListConversations)
			r.Post("/conversations", m.handleCreateConversation)
			r.Patch("/conversations/{roomId}", m.handlePatchConversation)
			r.Post("/conversations/{roomId}/dissolve", m.handleDissolveConversation)
			r.Get("/conversations/{roomId}/snapshot", m.handleSnapshot)
			r.Get("/conversations/{roomId}/members", m.handleListMembers)
			r.Post("/conversations/{roomId}/members", m.handleMemberAction)
			r.Post("/conversations/{roomId}/bot-access", m.handleBotAccess)
			r.Post("/conversations/{roomId}/nickname", m.handleNickname)
			r.Post("/conversations/{roomId}/transfer", m.handleTransfer)
			r.Post("/conversations/{roomId}/list-state", m.handleSetListState)
			r.Get("/conversation-list-state", m.handleGetListState)

			r.Get("/conversations/{roomId}/messages", m.handleListMessages)
			r.Post("/conversations/{roomId}/messages", m.handleSendMessage)
			r.Get("/conversations/{roomId}/messages/{messageId}", m.handleGetMessage)
			r.Post("/conversations/{roomId}/messages/{messageId}/continue", m.handleContinue)
			r.Post("/conversations/{roomId}/messages/{messageId}/cancel-planning", m.handleCancelPlanning)
			r.Post("/conversations/{roomId}/messages/{messageId}/reactions", m.handleAddReaction)
			r.Get("/conversations/{roomId}/messages/{messageId}/reactions", m.handleListReactions)

			r.Post("/executions/claim", m.handleClaimExecution)
			r.Post("/conversations/{roomId}/executions/{executionId}", m.handleExecutionAction)
			r.Post("/conversations/{roomId}/executions/{executionId}/control", m.handleExecutionControl)
			r.Get("/conversations/{roomId}/executions", m.handleListExecutions)
			r.Get("/conversations/{roomId}/execution-failures", m.handleExecutionFailures)

			r.Get("/conversations/{roomId}/plans", m.handleListPlans)
			r.Post("/conversations/{roomId}/plans", m.handleCreatePlan)
			r.Post("/conversations/{roomId}/plans/{planId}", m.handlePlanAction)
			r.Post("/conversations/{roomId}/plans/{planId}/messages", m.handlePlanMessage)

			r.Post("/media/prepare", m.handleMediaPrepare)
			r.Post("/media/{mediaId}/complete", m.handleMediaComplete)
			r.Get("/media/{mediaId}", m.handleMediaGet)
			// Current clients scope media under the conversation.
			r.Post("/conversations/{roomId}/media", m.handleConversationMediaPrepare)
			r.Post("/conversations/{roomId}/media/{mediaId}/complete", m.handleMediaComplete)
			r.Get("/conversations/{roomId}/media/{mediaId}", m.handleMediaGet)

			r.Get("/conversations/{roomId}/import", m.handleImportProgress)
			r.Post("/conversations/{roomId}/import", m.handleImportMessages)
			r.Get("/conversations/{roomId}/import/plan-source", m.handleImportPlanSource)
			r.Post("/conversations/{roomId}/invites", m.handleCreateInvite)
			r.Delete("/invites/{inviteId}", m.handleDeleteInvite)
			r.Get("/invites/{token}", m.handlePreviewInvite)
			r.Post("/invites/{token}/accept", m.handleAcceptInvite)
			// Current clients use invite-links with the token in the body.
			r.Post("/conversations/{roomId}/invite-links", m.handleCreateInvite)
			r.Post("/conversations/{roomId}/invite-links/revoke", m.handleRevokeInviteLink)
			r.Post("/invite-links/preview", m.handlePreviewInviteLink)
			r.Post("/invite-links/accept", m.handleAcceptInviteLink)

			r.Get("/changes", m.handleChanges)
		})
	})
	return r
}

func (m *Module) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := m.deps.DB.Ping(ctx); err != nil {
		httpapi.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false})
		return
	}
	httpapi.OK(w, map[string]any{"ok": true})
}

func (m *Module) fail(w http.ResponseWriter, err error) {
	var apiErr *httpapi.APIError
	if errors.As(err, &apiErr) {
		httpapi.WriteError(w, apiErr)
		return
	}
	m.logger.Error("chat_request_failed", "error", err)
	httpapi.WriteError(w, httpapi.ErrInternal)
}
