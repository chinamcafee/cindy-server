package modelaccess

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
	"github.com/makecindy/cindy-server/internal/platform/idempotency"
)

var idempotencyKeyRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

type billingPurchaseOption struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Capability    string `json:"capability"`
	PaymentAction string `json:"paymentAction"`
}

type billingOffer struct {
	Code              string                  `json:"code"`
	Name              string                  `json:"name,omitempty"`
	SalesState        string                  `json:"salesState"`
	Purchasable       bool                    `json:"purchasable"`
	UnavailableReason *string                 `json:"unavailableReason"`
	Interval          *string                 `json:"interval"`
	Currency          string                  `json:"currency"`
	Amount            *string                 `json:"amount"`
	MinAmount         *string                 `json:"minAmount"`
	MaxAmount         *string                 `json:"maxAmount"`
	CreditAmount      *string                 `json:"creditAmount"`
	RolloverCap       *string                 `json:"rolloverCap"`
	PurchaseOptions   []billingPurchaseOption `json:"purchaseOptions"`
}

type billingProduct struct {
	Code      string         `json:"code"`
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Level     *int           `json:"level"`
	SortOrder int            `json:"sortOrder"`
	Offers    []billingOffer `json:"offers"`
}

type planFeatures struct {
	Kind         string          `json:"kind"`
	Level        *int            `json:"level"`
	CreditAmount json.RawMessage `json:"creditAmount"`
	RolloverCap  json.RawMessage `json:"rolloverCap"`
}

type billingOrder struct {
	ID          string  `json:"id"`
	OrderID     string  `json:"orderId"`
	Status      string  `json:"status"`
	Amount      string  `json:"amount"`
	AmountMinor int64   `json:"amountMinor"`
	Currency    string  `json:"currency"`
	ProductCode string  `json:"productCode,omitempty"`
	OfferCode   string  `json:"offerCode,omitempty"`
	PlanID      *string `json:"planId,omitempty"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
}

type effectivePlan struct {
	Version    int              `json:"version"`
	Product    effectiveProduct `json:"product"`
	Offer      effectiveOffer   `json:"offer"`
	Terms      effectiveTerms   `json:"terms"`
	CapturedAt string           `json:"capturedAt"`
}

type effectiveProduct struct {
	Code  string `json:"code"`
	Kind  string `json:"kind"`
	Level int    `json:"level"`
}

type effectiveOffer struct {
	Code     string `json:"code"`
	Interval string `json:"interval"`
}

type effectiveTerms struct {
	Amount       string `json:"amount"`
	Currency     string `json:"currency"`
	CreditAmount string `json:"creditAmount"`
	RolloverCap  string `json:"rolloverCap"`
}

type billingSubscription struct {
	SubscriptionID        string         `json:"subscriptionId"`
	Status                string         `json:"status"`
	Provider              string         `json:"provider,omitempty"`
	CurrentPeriodStartAt  *string        `json:"currentPeriodStartAt"`
	CurrentPeriodEndAt    *string        `json:"currentPeriodEndAt"`
	EntitlementValidUntil *string        `json:"entitlementValidUntil"`
	CancelAtPeriodEnd     bool           `json:"cancelAtPeriodEnd"`
	Resumable             bool           `json:"resumable"`
	EffectivePlan         *effectivePlan `json:"effectivePlan"`
	PurchaseAttemptID     *string        `json:"purchaseAttemptId"`
	PaymentAction         any            `json:"paymentAction"`
	PendingPlanChange     any            `json:"pendingPlanChange"`
}

type billingPlanChange struct {
	PlanChangeID      string  `json:"planChangeId"`
	ChangeType        string  `json:"changeType"`
	Status            string  `json:"status"`
	QuotedAmountMinor *int64  `json:"quotedAmountMinor"`
	QuotedCurrency    *string `json:"quotedCurrency"`
	QuoteExpiresAt    *string `json:"quoteExpiresAt"`
	EffectiveAt       string  `json:"effectiveAt"`
	PaymentAction     any     `json:"paymentAction"`
}

type subRow struct {
	ID          string
	Status      string
	PlanID      string
	PlanSlug    string
	PlanName    string
	PriceMinor  int64
	Currency    string
	Interval    string
	Features    planFeatures
	PeriodStart *time.Time
	PeriodEnd   *time.Time
	CreatedAt   time.Time
}

func (f planFeatures) creditAmount() *string {
	return rawJSONString(f.CreditAmount)
}

func (f planFeatures) rolloverCap() *string {
	return rawJSONString(f.RolloverCap)
}

func billingCurrency(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func rawJSONString(raw json.RawMessage) *string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return &s
	}
	return nil
}

func normalizeInterval(v string) *string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "MONTH", "monthly", "month":
		s := "MONTH"
		return &s
	case "YEAR", "yearly", "year", "annual":
		s := "YEAR"
		return &s
	default:
		return nil
	}
}

func planKind(f planFeatures) string {
	switch strings.ToUpper(f.Kind) {
	case "CREDIT_TOPUP":
		return "CREDIT_TOPUP"
	case "SUBSCRIPTION":
		return "SUBSCRIPTION"
	default:
		return "SUBSCRIPTION"
	}
}

func (m *Module) billingCatalog(w http.ResponseWriter, r *http.Request) {
	products := []billingProduct{}
	if m.db != nil {
		rows, err := m.db.Pool.Query(r.Context(), `
			SELECT slug, name, price_minor, currency, interval, features
			FROM ma_plans WHERE status = 'active'
			ORDER BY price_minor, slug`)
		if err != nil {
			httpapi.WriteError(w, httpapi.ErrInternal)
			return
		}
		defer rows.Close()
		index := 0
		for rows.Next() {
			var slug, name, currency, interval string
			var price int64
			var rawFeatures []byte
			if err := rows.Scan(&slug, &name, &price, &currency, &interval, &rawFeatures); err != nil {
				httpapi.WriteError(w, httpapi.ErrInternal)
				return
			}
			var features planFeatures
			_ = json.Unmarshal(rawFeatures, &features)
			amount := amountString(price)
			level := 0
			if features.Level != nil {
				level = *features.Level
			}
			creditAmount := "0"
			if v := features.creditAmount(); v != nil {
				creditAmount = *v
			}
			rolloverCap := "0"
			if v := features.rolloverCap(); v != nil {
				rolloverCap = *v
			}
			product := billingProduct{
				Code:      slug,
				Name:      name,
				Kind:      planKind(features),
				Level:     &level,
				SortOrder: index,
				Offers: []billingOffer{{
					Code:              slug,
					Name:              name,
					SalesState:        "AVAILABLE",
					Purchasable:       true,
					UnavailableReason: nil,
					Interval:          normalizeInterval(interval),
					Currency:          billingCurrency(currency),
					Amount:            &amount,
					CreditAmount:      &creditAmount,
					RolloverCap:       &rolloverCap,
					PurchaseOptions: []billingPurchaseOption{{
						ID:            slug + ":alipay",
						Provider:      "alipay",
						Capability:    "PROVIDER_MANAGED_SUBSCRIPTION",
						PaymentAction: "REDIRECT",
					}},
				}},
			}
			products = append(products, product)
			index++
		}
		if err := rows.Err(); err != nil {
			httpapi.WriteError(w, httpapi.ErrInternal)
			return
		}
	}
	httpapi.OK(w, map[string]any{"products": products})
}

type planRow struct {
	ID         string
	Slug       string
	Name       string
	PriceMinor int64
	Currency   string
	Interval   string
	Features   planFeatures
}

func (m *Module) loadPlan(ctx context.Context, offerCode string) (*planRow, error) {
	if m.db == nil {
		return nil, pgx.ErrNoRows
	}
	var p planRow
	var rawFeatures []byte
	err := m.db.Pool.QueryRow(ctx, `
		SELECT id::text, slug, name, price_minor, currency, interval, features
		FROM ma_plans WHERE slug = $1 AND status = 'active' LIMIT 1`, offerCode).
		Scan(&p.ID, &p.Slug, &p.Name, &p.PriceMinor, &p.Currency, &p.Interval, &rawFeatures)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(rawFeatures, &p.Features)
	return &p, nil
}

func validIdempotencyKey(key string) bool {
	return key == "" || idempotencyKeyRe.MatchString(key)
}

func (m *Module) idempotent(w http.ResponseWriter, r *http.Request, scope, key string, request any, fn func(context.Context) (int, any, error)) {
	if m.idem == nil || key == "" {
		status, body, err := fn(r.Context())
		writeResult(w, status, body, err)
		return
	}
	res, err := m.idem.Do(r.Context(), scope, key, idempotency.Hash(request), fn)
	if err != nil {
		switch {
		case errors.Is(err, idempotency.ErrConflict):
			httpapi.WriteError(w, conflict("IDEMPOTENCY_CONFLICT", "idempotency key reused with a different request"))
		case errors.Is(err, idempotency.ErrInProgress):
			httpapi.WriteError(w, conflict("IDEMPOTENCY_IN_PROGRESS", "request is still in progress"))
		default:
			var apiErr *httpapi.APIError
			if errors.As(err, &apiErr) {
				httpapi.WriteError(w, apiErr)
				return
			}
			httpapi.WriteError(w, httpapi.ErrInternal)
		}
		return
	}
	var body any
	if err := json.Unmarshal(res.Body, &body); err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	writeResult(w, res.StatusCode, body, nil)
}

func writeResult(w http.ResponseWriter, status int, body any, err error) {
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, status, body)
}

func (m *Module) createTopupOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OfferCode        string `json:"offerCode"`
		Amount           string `json:"amount"`
		PurchaseOptionID string `json:"purchaseOptionId"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.OfferCode = strings.TrimSpace(req.OfferCode)
	if req.OfferCode == "" || len(req.OfferCode) > 128 {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "offerCode is required"))
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !validIdempotencyKey(key) {
		httpapi.WriteError(w, badRequest("INVALID_IDEMPOTENCY_KEY", "Idempotency-Key is invalid"))
		return
	}
	p := httpapi.MustPrincipal(r.Context())
	plan, err := m.loadPlan(r.Context(), req.OfferCode)
	if err != nil {
		httpapi.WriteError(w, notFound("OFFER_NOT_FOUND", "offer not found"))
		return
	}
	scope := "billing.topup:" + p.AccountID
	m.idempotent(w, r, scope, key, req, func(ctx context.Context) (int, any, error) {
		if key != "" {
			if existing, err := m.findOrderByKey(ctx, p.AccountID, key); err == nil && existing != nil {
				return http.StatusOK, existing, nil
			}
		}
		order, err := m.insertOrder(ctx, p.AccountID, plan, key)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, order, nil
	})
}

func normalizeBillingOrder(order *billingOrder) {
	order.Status = strings.ToUpper(strings.TrimSpace(order.Status))
	order.Currency = billingCurrency(order.Currency)
}

func (m *Module) insertOrder(ctx context.Context, accountID string, plan *planRow, key string) (*billingOrder, error) {
	var storedKey any
	if key != "" {
		storedKey = accountID + ":" + key
	}
	var planID *string
	if plan.ID != "" {
		id := plan.ID
		planID = &id
	}
	var order billingOrder
	var createdAt, updatedAt time.Time
	err := m.db.Pool.QueryRow(ctx, `
		INSERT INTO ma_orders (account_id, plan_id, amount_minor, currency, status, idempotency_key)
		VALUES ($1::uuid, $2::uuid, $3, $4, 'pending', $5)
		RETURNING id::text, status, amount_minor, currency, created_at, updated_at`,
		accountID, plan.ID, plan.PriceMinor, plan.Currency, storedKey).
		Scan(&order.ID, &order.Status, &order.AmountMinor, &order.Currency, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	order.OrderID = order.ID
	order.Amount = amountString(order.AmountMinor)
	order.PlanID = planID
	order.OfferCode = plan.Slug
	order.ProductCode = plan.Slug
	order.CreatedAt = isoMillis(createdAt)
	order.UpdatedAt = isoMillis(updatedAt)
	normalizeBillingOrder(&order)
	return &order, nil
}

func (m *Module) findOrderByKey(ctx context.Context, accountID, key string) (*billingOrder, error) {
	if m.db == nil {
		return nil, pgx.ErrNoRows
	}
	var order billingOrder
	var createdAt, updatedAt time.Time
	var planID string
	err := m.db.Pool.QueryRow(ctx, `
		SELECT o.id::text, o.status, o.amount_minor, o.currency, o.created_at, o.updated_at,
		       COALESCE(p.slug,''), COALESCE(o.plan_id::text,'')
		FROM ma_orders o LEFT JOIN ma_plans p ON p.id = o.plan_id
		WHERE o.account_id::text = $1 AND o.idempotency_key = $2
		LIMIT 1`, accountID, accountID+":"+key).
		Scan(&order.ID, &order.Status, &order.AmountMinor, &order.Currency, &createdAt, &updatedAt, &order.OfferCode, &planID)
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

func (m *Module) getOrder(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	id := strings.TrimSpace(chiURLParam(r, "id"))
	if m.db == nil || !isUUID(id) {
		httpapi.WriteError(w, notFound("ORDER_NOT_FOUND", "order not found"))
		return
	}
	var order billingOrder
	var createdAt, updatedAt time.Time
	var planID string
	err := m.db.Pool.QueryRow(r.Context(), `
		SELECT o.id::text, o.status, o.amount_minor, o.currency, o.created_at, o.updated_at,
		       COALESCE(o.plan_id::text,''), COALESCE(p.slug,'')
		FROM ma_orders o LEFT JOIN ma_plans p ON p.id = o.plan_id
		WHERE o.id::text = $1 AND o.account_id::text = $2`, id, p.AccountID).
		Scan(&order.ID, &order.Status, &order.AmountMinor, &order.Currency, &createdAt, &updatedAt, &planID, &order.OfferCode)
	if err != nil {
		httpapi.WriteError(w, notFound("ORDER_NOT_FOUND", "order not found"))
		return
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
	httpapi.OK(w, order)
}

func (m *Module) billingListOrders(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	orders := []billingOrder{}
	if m.db != nil {
		rows, err := m.db.Pool.Query(r.Context(), `
			SELECT o.id::text, o.status, o.amount_minor, o.currency, o.created_at, o.updated_at,
			       COALESCE(o.plan_id::text,''), COALESCE(p.slug,'')
			FROM ma_orders o LEFT JOIN ma_plans p ON p.id = o.plan_id
			WHERE o.account_id::text = $1
			ORDER BY o.created_at DESC
			LIMIT $2`, p.AccountID, limit)
		if err != nil {
			httpapi.WriteError(w, httpapi.ErrInternal)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var order billingOrder
			var createdAt, updatedAt time.Time
			var planID string
			if err := rows.Scan(&order.ID, &order.Status, &order.AmountMinor, &order.Currency,
				&createdAt, &updatedAt, &planID, &order.OfferCode); err != nil {
				httpapi.WriteError(w, httpapi.ErrInternal)
				return
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
			orders = append(orders, order)
		}
		if err := rows.Err(); err != nil {
			httpapi.WriteError(w, httpapi.ErrInternal)
			return
		}
	}
	httpapi.OK(w, map[string]any{"orders": orders, "nextCursor": nil})
}

func (m *Module) loadSubscriptions(ctx context.Context, accountID string, currentOnly bool) ([]subRow, error) {
	if m.db == nil {
		return nil, nil
	}
	query := `
		SELECT s.id::text, s.status, s.plan_id::text, p.slug, p.name, p.price_minor,
		       p.currency, p.interval, p.features, s.current_period_start,
		       s.current_period_end, s.created_at
		FROM ma_subscriptions s
		JOIN ma_plans p ON p.id = s.plan_id
		WHERE s.account_id::text = $1`
	if currentOnly {
		query += ` ORDER BY (s.status = 'canceled'), s.created_at DESC LIMIT 1`
	} else {
		query += ` ORDER BY s.created_at DESC`
	}
	rows, err := m.db.Pool.Query(ctx, query, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []subRow
	for rows.Next() {
		var row subRow
		var rawFeatures []byte
		if err := rows.Scan(&row.ID, &row.Status, &row.PlanID, &row.PlanSlug, &row.PlanName,
			&row.PriceMinor, &row.Currency, &row.Interval, &rawFeatures,
			&row.PeriodStart, &row.PeriodEnd, &row.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(rawFeatures, &row.Features)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (m *Module) subscriptionJSON(row subRow) billingSubscription {
	interval := "MONTH"
	if v := normalizeInterval(row.Interval); v != nil {
		interval = *v
	}
	level := 1
	if row.Features.Level != nil {
		level = *row.Features.Level
	}
	creditAmount := "0"
	if v := row.Features.creditAmount(); v != nil {
		creditAmount = *v
	}
	rolloverCap := "0"
	if v := row.Features.rolloverCap(); v != nil {
		rolloverCap = *v
	}
	now := m.now()
	resumable := row.Status == "canceled" && row.PeriodEnd != nil && row.PeriodEnd.After(now)
	return billingSubscription{
		SubscriptionID:        row.ID,
		Status:                strings.ToUpper(row.Status),
		Provider:              "mock",
		CurrentPeriodStartAt:  isoPtr(row.PeriodStart),
		CurrentPeriodEndAt:    isoPtr(row.PeriodEnd),
		EntitlementValidUntil: isoPtr(row.PeriodEnd),
		CancelAtPeriodEnd:     row.Status == "canceled",
		Resumable:             resumable,
		EffectivePlan: &effectivePlan{
			Version:    1,
			Product:    effectiveProduct{Code: row.PlanSlug, Kind: "SUBSCRIPTION", Level: level},
			Offer:      effectiveOffer{Code: row.PlanSlug, Interval: interval},
			Terms:      effectiveTerms{Amount: amountString(row.PriceMinor), Currency: billingCurrency(row.Currency), CreditAmount: creditAmount, RolloverCap: rolloverCap},
			CapturedAt: isoMillis(row.CreatedAt),
		},
		PurchaseAttemptID: nil,
		PaymentAction:     nil,
		PendingPlanChange: nil,
	}
}

func (m *Module) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	rows, err := m.loadSubscriptions(r.Context(), p.AccountID, false)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	out := make([]billingSubscription, 0, len(rows))
	for _, row := range rows {
		out = append(out, m.subscriptionJSON(row))
	}
	httpapi.OK(w, map[string]any{"subscriptions": out, "nextCursor": nil})
}

func (m *Module) currentSubscription(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	rows, err := m.loadSubscriptions(r.Context(), p.AccountID, true)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	if len(rows) == 0 {
		httpapi.OK(w, map[string]any{"subscription": nil})
		return
	}
	httpapi.OK(w, map[string]any{"subscription": m.subscriptionJSON(rows[0])})
}

func (m *Module) createSubscription(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OfferCode        string `json:"offerCode"`
		PurchaseOptionID string `json:"purchaseOptionId"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.OfferCode = strings.TrimSpace(req.OfferCode)
	if req.OfferCode == "" || len(req.OfferCode) > 128 {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "offerCode is required"))
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !validIdempotencyKey(key) {
		httpapi.WriteError(w, badRequest("INVALID_IDEMPOTENCY_KEY", "Idempotency-Key is invalid"))
		return
	}
	p := httpapi.MustPrincipal(r.Context())
	plan, err := m.loadPlan(r.Context(), req.OfferCode)
	if err != nil {
		httpapi.WriteError(w, notFound("OFFER_NOT_FOUND", "offer not found"))
		return
	}
	scope := "billing.subscription:" + p.AccountID
	m.idempotent(w, r, scope, key, req, func(ctx context.Context) (int, any, error) {
		var exists bool
		if err := m.db.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM ma_subscriptions
				WHERE account_id::text = $1 AND status IN ('trialing','active','past_due')
			)`, p.AccountID).Scan(&exists); err != nil {
			return 0, nil, httpapi.ErrInternal
		}
		if exists {
			return 0, nil, conflict("SUBSCRIPTION_EXISTS", "an active subscription already exists")
		}
		end := m.now()
		if iv := normalizeInterval(plan.Interval); iv != nil && *iv == "YEAR" {
			end = end.AddDate(1, 0, 0)
		} else {
			end = end.AddDate(0, 1, 0)
		}
		if _, err := m.db.Pool.Exec(ctx, `
			INSERT INTO ma_subscriptions (account_id, plan_id, status, current_period_start, current_period_end)
			VALUES ($1::uuid, $2::uuid, 'active', $3, $4)`,
			p.AccountID, plan.ID, m.now(), end); err != nil {
			return 0, nil, httpapi.ErrInternal
		}
		rows, err := m.loadSubscriptions(ctx, p.AccountID, true)
		if err != nil || len(rows) == 0 {
			return 0, nil, httpapi.ErrInternal
		}
		return http.StatusOK, m.subscriptionJSON(rows[0]), nil
	})
}

func (m *Module) resumeSubscription(w http.ResponseWriter, r *http.Request) {
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
	if row.Status == "canceled" {
		if row.PeriodEnd == nil || !row.PeriodEnd.After(m.now()) {
			httpapi.WriteError(w, conflict("RESUME_NOT_AVAILABLE", "subscription can no longer be resumed"))
			return
		}
		if _, err := m.db.Pool.Exec(r.Context(), `
			UPDATE ma_subscriptions SET status = 'active', updated_at = now()
			WHERE id::text = $1 AND account_id::text = $2`, row.ID, p.AccountID); err != nil {
			httpapi.WriteError(w, httpapi.ErrInternal)
			return
		}
		row.Status = "active"
	}
	httpapi.OK(w, m.subscriptionJSON(row))
}

func (m *Module) planChangeQuote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TargetOfferCode string `json:"targetOfferCode"`
	}
	if err := httpapi.DecodeJSONLoose(w, r, &req); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	req.TargetOfferCode = strings.TrimSpace(req.TargetOfferCode)
	if req.TargetOfferCode == "" || len(req.TargetOfferCode) > 128 {
		httpapi.WriteError(w, badRequest("INVALID_PARAMS", "targetOfferCode is required"))
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !validIdempotencyKey(key) {
		httpapi.WriteError(w, badRequest("INVALID_IDEMPOTENCY_KEY", "Idempotency-Key is invalid"))
		return
	}
	p := httpapi.MustPrincipal(r.Context())
	rows, err := m.loadSubscriptions(r.Context(), p.AccountID, true)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	if len(rows) == 0 || rows[0].Status == "canceled" {
		httpapi.WriteError(w, conflict("PLAN_CHANGE_NOT_AVAILABLE", "no active subscription"))
		return
	}
	current := rows[0]
	target, err := m.loadPlan(r.Context(), req.TargetOfferCode)
	if err != nil {
		httpapi.WriteError(w, notFound("OFFER_NOT_FOUND", "offer not found"))
		return
	}
	scope := "billing.plan_change:" + p.AccountID
	m.idempotent(w, r, scope, key, req, func(ctx context.Context) (int, any, error) {
		id := uuid.NewString()
		now := m.now()
		diff := target.PriceMinor - current.PriceMinor
		changeType := "DOWNGRADE"
		if diff > 0 {
			changeType = "UPGRADE"
		}
		if diff < 0 {
			diff = -diff
		}
		effective := now.AddDate(0, 1, 0)
		if current.PeriodEnd != nil && current.PeriodEnd.After(now) {
			effective = *current.PeriodEnd
		}
		expires := now.Add(15 * time.Minute)
		quotedCurrency := billingCurrency(target.Currency)
		quote := billingPlanChange{
			PlanChangeID:      id,
			ChangeType:        changeType,
			Status:            "QUOTED",
			QuotedAmountMinor: &diff,
			QuotedCurrency:    &quotedCurrency,
			QuoteExpiresAt:    isoMillisPtr(expires),
			EffectiveAt:       isoMillis(effective),
			PaymentAction:     nil,
		}
		if m.rdb != nil {
			if raw, err := json.Marshal(quote); err == nil {
				_ = m.rdb.Set(ctx, "cindy:ma:planchange:"+id, raw, 15*time.Minute).Err()
			}
		}
		return http.StatusOK, quote, nil
	})
}

func (m *Module) billingPortal(w http.ResponseWriter, r *http.Request) {
	base := "http://127.0.0.1:19828"
	if m.cfg != nil && m.cfg.PublicBaseURL != "" {
		base = strings.TrimRight(m.cfg.PublicBaseURL, "/")
	}
	httpapi.OK(w, map[string]any{"url": base + "/billing/portal"})
}
