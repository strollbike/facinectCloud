package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/facinect/paygateway/internal/config"
)

type Service struct {
	DB       *sql.DB
	Cfg      config.Config
	Registry *Registry
}

func (s *Service) Providers() []ProviderInfo {
	return s.Registry.List()
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (*CreateResult, error) {
	p, err := s.Registry.Get(req.Provider)
	if err != nil {
		return nil, err
	}
	// Unconfigured provider (e.g. razorpay without keys) → fall back to default/stub.
	if !p.Configured() {
		p, err = s.Registry.Default()
		if err != nil {
			return nil, fmt.Errorf("provider_unavailable")
		}
	}

	normalizeCreate(&req)
	if req.AmountPaise <= 0 {
		return nil, fmt.Errorf("invalid_amount")
	}

	res, err := p.CreateOrder(ctx, req)
	if err != nil {
		return nil, err
	}

	metaJSON, _ := json.Marshal(req.Metadata)
	checkoutJSON, _ := json.Marshal(res.Checkout)
	var facility interface{}
	if req.FacilityID != nil {
		facility = *req.FacilityID
	}
	var id int64
	err = s.DB.QueryRowContext(ctx, `
INSERT INTO gateway_orders
  (provider, external_order_id, amount_paise, currency, receipt, status, facility_id, purpose, metadata, checkout_json)
VALUES ($1,$2,$3,$4,NULLIF($5,''),'created',$6,NULLIF($7,''),$8::jsonb,$9::jsonb)
RETURNING id`,
		res.Provider, res.ExternalOrderID, res.AmountPaise, res.Currency, res.Receipt,
		facility, req.Purpose, string(metaJSON), string(checkoutJSON),
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	res.OrderID = strconv.FormatInt(id, 10)
	return res, nil
}

func (s *Service) CreatePaymentLink(ctx context.Context, req PaymentLinkRequest) (*PaymentLinkResult, error) {
	p, err := s.Registry.Get(req.Provider)
	if err != nil {
		return nil, err
	}
	if !p.Configured() {
		p, err = s.Registry.Default()
		if err != nil {
			return nil, fmt.Errorf("provider_unavailable")
		}
	}
	paise := req.AmountPaise
	if paise <= 0 && req.Amount > 0 {
		paise = int64(req.Amount*100 + 0.5)
	}
	req.AmountPaise = paise
	if req.AmountPaise <= 0 {
		return nil, fmt.Errorf("invalid_amount")
	}
	if strings.TrimSpace(req.Currency) == "" {
		req.Currency = "INR"
	}
	return p.CreatePaymentLink(ctx, req)
}

func (s *Service) CancelPaymentLink(ctx context.Context, linkID string) error {
	linkID = strings.TrimSpace(linkID)
	if linkID == "" {
		return fmt.Errorf("invalid_link_id")
	}
	p, err := s.Registry.Default()
	if err != nil {
		return err
	}
	// Prefer razorpay when link looks like plink_* and provider is configured.
	if strings.HasPrefix(linkID, "plink_") {
		if rp, e := s.Registry.Get("razorpay"); e == nil && rp.Configured() {
			p = rp
		}
	}
	if strings.HasPrefix(linkID, "plink_stub_") {
		if sp, e := s.Registry.Get("stub"); e == nil {
			p = sp
		}
	}
	return p.CancelPaymentLink(ctx, linkID)
}

func (s *Service) Verify(ctx context.Context, req VerifyRequest) (*VerifyResult, error) {
	p, err := s.Registry.Get(req.Provider)
	if err != nil {
		return nil, err
	}
	if req.Provider == "" {
		// try infer from DB by external order id
		if req.ExternalOrderID != "" {
			var prov string
			_ = s.DB.QueryRowContext(ctx, `
SELECT provider FROM gateway_orders WHERE external_order_id=$1 ORDER BY id DESC LIMIT 1`,
				req.ExternalOrderID).Scan(&prov)
			if prov != "" {
				p, err = s.Registry.Get(prov)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	res, err := p.Verify(ctx, req)
	if err != nil {
		return nil, err
	}
	if res.Valid && req.ExternalOrderID != "" {
		_, _ = s.DB.ExecContext(ctx, `
UPDATE gateway_orders SET status='paid', updated_at=NOW()
WHERE external_order_id=$1 AND provider=$2`, req.ExternalOrderID, p.Name())
	}
	return res, nil
}

func (s *Service) HandleWebhook(ctx context.Context, providerName string, headers http.Header, body []byte) (*NormalizedEvent, error) {
	p, err := s.Registry.Get(providerName)
	if err != nil {
		return nil, err
	}
	ev, err := p.ParseWebhook(headers, body)
	if err != nil {
		return nil, err
	}
	if ev.ExternalOrderID != "" {
		st := "created"
		switch ev.Type {
		case "payment.captured":
			st = "paid"
		case "payment.failed":
			st = "failed"
		case "refunded":
			st = "refunded"
		}
		if st != "created" {
			_, _ = s.DB.ExecContext(ctx, `
UPDATE gateway_orders SET status=$1, updated_at=NOW()
WHERE external_order_id=$2 AND provider=$3`, st, ev.ExternalOrderID, p.Name())
		}
	}
	return ev, nil
}

func normalizeCreate(req *CreateRequest) {
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	if req.Currency == "" {
		req.Currency = "INR"
	}
	req.Receipt = strings.TrimSpace(req.Receipt)
	req.Purpose = strings.TrimSpace(req.Purpose)
	if req.AmountPaise <= 0 && req.Amount > 0 {
		req.AmountPaise = int64(req.Amount*100 + 0.5)
	}
	if req.FacilityID == nil && req.Metadata != nil {
		if v, ok := req.Metadata["facility_id"]; ok {
			switch n := v.(type) {
			case float64:
				id := int64(n)
				req.FacilityID = &id
			case json.Number:
				if i, err := n.Int64(); err == nil {
					req.FacilityID = &i
				}
			}
		}
	}
	if req.Purpose == "" && req.Metadata != nil {
		if v, ok := req.Metadata["purpose"].(string); ok {
			req.Purpose = v
		}
	}
}
