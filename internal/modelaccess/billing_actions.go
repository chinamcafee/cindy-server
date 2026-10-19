package modelaccess

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

func (m *Module) loadBillingOrder(ctx context.Context, accountID, id string) (*billingOrder, error) {
	var order billingOrder
	var createdAt, updatedAt time.Time
	var planID string
	err := m.db.Pool.QueryRow(ctx, `
		SELECT o.id::text, o.status, o.amount_minor, o.currency, o.created_at, o.updated_at,
		       COALESCE(o.plan_id::text,''), COALESCE(p.slug,'')
		FROM ma_orders o LEFT JOIN ma_plans p ON p.id = o.plan_id
		WHERE o.id::text = $1 AND o.account_id::text = $2`, id, accountID).
		Scan(&order.ID, &order.Status, &order.AmountMinor, &order.Currency, &createdAt, &updatedAt, &planID, &order.OfferCode)
	if err != nil {
		return nil, err
	}
	order.OrderID = order.ID
	order.Amount = amountString(order.AmountMinor)
	order.ProductCode = order.OfferCode
	if planID != "" {
		order.PlanID = &planID
	}
	order.CreatedAt = isoMillis(createdAt)
	order.UpdatedAt = isoMillis(updatedAt)
	normalizeBillingOrder(&order)
	return &order, nil
}

func (m *Module) billingOrderAction(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	action := strings.TrimSpace(chi.URLParam(r, "action"))
	if !isUUID(id) {
		httpapi.WriteError(w, notFound("ORDER_NOT_FOUND", "order not found"))
		return
	}
	switch action {
	case "cancel":
		_, err := m.db.Pool.Exec(r.Context(), `
			UPDATE ma_orders SET status='canceled', updated_at=now()
			WHERE id=$1::uuid AND account_id=$2::uuid AND status IN ('pending','created')`, id, p.AccountID)
		if err != nil {
			httpapi.WriteError(w, httpapi.ErrInternal)
			return
		}
	case "refresh", "retry":
	default:
		httpapi.WriteError(w, httpapi.ErrNotFound)
		return
	}
	order, err := m.loadBillingOrder(r.Context(), p.AccountID, id)
	if err != nil {
		if err == pgx.ErrNoRows {
			httpapi.WriteError(w, notFound("ORDER_NOT_FOUND", "order not found"))
			return
		}
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	httpapi.OK(w, order)
}

func (m *Module) cancelCurrentSubscription(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	rows, err := m.loadSubscriptions(r.Context(), p.AccountID, true)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	if len(rows) == 0 {
		httpapi.WriteError(w, notFound("SUBSCRIPTION_NOT_FOUND", "subscription not found"))
		return
	}
	row := rows[0]
	if _, err := m.db.Pool.Exec(r.Context(), `
		UPDATE ma_subscriptions SET status='canceled', updated_at=now()
		WHERE id::text=$1 AND account_id::text=$2`, row.ID, p.AccountID); err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	row.Status = "canceled"
	httpapi.OK(w, m.subscriptionJSON(row))
}

func (m *Module) refreshSubscriptionPurchase(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	rows, err := m.loadSubscriptions(r.Context(), p.AccountID, true)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	if len(rows) == 0 {
		httpapi.WriteError(w, notFound("SUBSCRIPTION_NOT_FOUND", "subscription not found"))
		return
	}
	httpapi.OK(w, m.subscriptionJSON(rows[0]))
}

func (m *Module) planChangeAction(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if !isUUID(id) || m.rdb == nil {
		httpapi.WriteError(w, notFound("PLAN_CHANGE_NOT_FOUND", "plan change not found"))
		return
	}
	raw, err := m.rdb.Get(r.Context(), "cindy:ma:planchange:"+id).Bytes()
	if err != nil {
		httpapi.WriteError(w, notFound("PLAN_CHANGE_NOT_FOUND", "plan change not found"))
		return
	}
	var change billingPlanChange
	if err := json.Unmarshal(raw, &change); err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	switch {
	case r.Method == http.MethodDelete || strings.HasSuffix(r.URL.Path, "/cancel"):
		change.Status = "CANCELED"
	case strings.HasSuffix(r.URL.Path, "/confirm"):
		change.Status = "APPLIED"
	default:
		// refresh returns the stored quote unchanged
	}
	if encoded, err := json.Marshal(change); err == nil {
		_ = m.rdb.Set(r.Context(), "cindy:ma:planchange:"+id, encoded, 15*time.Minute).Err()
	}
	httpapi.OK(w, change)
}
