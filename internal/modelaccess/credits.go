package modelaccess

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/makecindy/cindy-server/internal/platform/httpapi"
)

type quotaResponse struct {
	Spend         float64  `json:"spend"`
	MaxBudget     *float64 `json:"maxBudget"`
	Currency      string   `json:"currency"`
	BudgetResetAt *string  `json:"budgetResetAt"`
}

type balanceResponse struct {
	PlanCredits        string `json:"planCredits"`
	PurchasedCredits   string `json:"purchasedCredits"`
	PromotionalCredits string `json:"promotionalCredits"`
	Available          string `json:"available"`
	Scale              int    `json:"scale"`
	ObservedAt         string `json:"observedAt"`
}

type creditPool struct {
	Remaining string  `json:"remaining"`
	Used      *string `json:"used"`
	Total     *string `json:"total"`
}

type creditUsageResponse struct {
	Available                   string     `json:"available"`
	Plan                        creditPool `json:"plan"`
	Purchased                   creditPool `json:"purchased"`
	Promotional                 creditPool `json:"promotional"`
	PromotionalGrants           []any      `json:"promotionalGrants"`
	PromotionalGrantsComplete   bool       `json:"promotionalGrantsComplete"`
	PromotionalGrantConsistency string     `json:"promotionalGrantConsistency"`
	LedgerUpdatedAt             *string    `json:"ledgerUpdatedAt"`
	Scale                       int        `json:"scale"`
	ObservedAt                  string     `json:"observedAt"`
}

func (m *Module) quota(w http.ResponseWriter, r *http.Request) {
	res := quotaResponse{Currency: "USD"}
	p := httpapi.MustPrincipal(r.Context())
	targets := []struct {
		scope  string
		target string
	}{{scope: "account", target: p.AccountID}}
	if orgID := m.membershipOrg(r.Context(), p.MembershipID); orgID != "" {
		targets = append(targets, struct {
			scope  string
			target string
		}{scope: "org", target: orgID})
	}

	if m.db != nil {
		for _, t := range targets {
			if !isUUID(t.target) {
				continue
			}
			var usedText, limitText, currency string
			var resetAt *time.Time
			err := m.db.Pool.QueryRow(r.Context(), `
				SELECT COALESCE(used_value,0)::text, COALESCE(limit_value,0)::text, currency, reset_at
				FROM ma_quota_limits
				WHERE scope = $1 AND target::text = $2 AND period = 'month'
				LIMIT 1`, t.scope, t.target).Scan(&usedText, &limitText, &currency, &resetAt)
			if err != nil {
				if err == pgx.ErrNoRows {
					continue
				}
				httpapi.WriteError(w, httpapi.ErrInternal)
				return
			}
			res.Spend = parseDecimalNumber(usedText)
			limit := parseDecimalNumber(limitText)
			if limit > 0 {
				res.MaxBudget = &limit
			}
			if currency != "" {
				res.Currency = currency
			}
			res.BudgetResetAt = isoPtr(resetAt)
			break
		}
	}
	httpapi.OK(w, res)
}

func (m *Module) creditSnapshot(ctx context.Context, accountID string) (available, plan, purchased, promotional int64, updated *time.Time, err error) {
	if m.db == nil || !isUUID(accountID) {
		return 0, 0, 0, 0, nil, nil
	}
	err = m.db.Pool.QueryRow(ctx, `
		SELECT
			COALESCE((SELECT balance_after FROM ma_credit_ledger
				WHERE account_id::text = $1 ORDER BY created_at DESC, id DESC LIMIT 1),0),
			COALESCE(SUM(CASE WHEN lower(reason) LIKE 'plan%' THEN delta END),0),
			COALESCE(SUM(CASE WHEN lower(reason) LIKE 'purchase%' OR lower(reason) LIKE 'topup%' THEN delta END),0),
			COALESCE(SUM(CASE WHEN lower(reason) LIKE 'promo%' THEN delta END),0),
			MAX(created_at)
		FROM ma_credit_ledger WHERE account_id::text = $1`, accountID).
		Scan(&available, &plan, &purchased, &promotional, &updated)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	plan = clampNonNegative(plan)
	promotional = clampNonNegative(promotional)
	purchased = available - plan - promotional
	return available, plan, purchased, promotional, updated, nil
}

func (m *Module) balance(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	available, plan, purchased, promotional, _, err := m.creditSnapshot(r.Context(), p.AccountID)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	httpapi.OK(w, balanceResponse{
		PlanCredits:        creditString(plan),
		PurchasedCredits:   creditString(purchased),
		PromotionalCredits: creditString(promotional),
		Available:          creditString(available),
		Scale:              decimalScale,
		ObservedAt:         isoMillis(m.now()),
	})
}

func (m *Module) creditUsage(w http.ResponseWriter, r *http.Request) {
	p := httpapi.MustPrincipal(r.Context())
	available, plan, purchased, promotional, updated, err := m.creditSnapshot(r.Context(), p.AccountID)
	if err != nil {
		httpapi.WriteError(w, httpapi.ErrInternal)
		return
	}
	promotionalPool := creditPool{Remaining: creditString(promotional)}
	grantsComplete := false
	if promotional == 0 {
		zero := creditString(0)
		promotionalPool.Used = &zero
		promotionalPool.Total = &zero
		grantsComplete = true
	}
	httpapi.OK(w, creditUsageResponse{
		Available:                   creditString(available),
		Plan:                        creditPool{Remaining: creditString(plan)},
		Purchased:                   creditPool{Remaining: creditString(purchased)},
		Promotional:                 promotionalPool,
		PromotionalGrants:           []any{},
		PromotionalGrantsComplete:   grantsComplete,
		PromotionalGrantConsistency: "OBSERVED",
		LedgerUpdatedAt:             isoPtr(updated),
		Scale:                       decimalScale,
		ObservedAt:                  isoMillis(m.now()),
	})
}
