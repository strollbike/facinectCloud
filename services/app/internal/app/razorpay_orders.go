package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// createPaymentOrder creates a Razorpay order via paygateway (preferred) or Razorpay REST.
// Falls back to a local order id when secrets are not configured (dev only).
func (s *Service) createPaymentOrder(ctx context.Context, amountINR float64, receipt, category string, notes map[string]any) (orderID, keyID string, amount float64, err error) {
	keyID = strings.TrimSpace(s.Cfg.RazorpayKeyID)
	amount = amountINR
	if amountINR < 0 {
		amountINR = 0
	}
	paise := int64(amountINR*100 + 0.5)
	if paise < 100 && amountINR > 0 {
		paise = 100
	}
	if receipt == "" {
		receipt = fmt.Sprintf("rcpt_%d", time.Now().Unix())
	}
	if notes == nil {
		notes = map[string]any{}
	}
	notes["category"] = category

	if s.Cfg.PaygatewayURL != "" {
		// paygateway: `amount` = INR rupees, `amount_paise` = preferred.
		// Sending paise in `amount` caused 80 → 8000 → ×100 again = ₹8000.
		payload := map[string]any{
			"provider":     "razorpay",
			"amount":       amountINR,
			"amount_paise": paise,
			"currency":     "INR",
			"receipt":      receipt,
			"notes":        notes,
			"customer_id":  fmt.Sprint(notes["userId"]),
		}
		raw, _ := json.Marshal(payload)
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, s.Cfg.PaygatewayURL+"/v1/gateway/orders", bytes.NewReader(raw))
		if reqErr == nil {
			req.Header.Set("Content-Type", "application/json")
			if s.Cfg.PaygatewayKey != "" {
				req.Header.Set("X-Service-Key", s.Cfg.PaygatewayKey)
			}
			res, doErr := http.DefaultClient.Do(req)
			if doErr == nil {
				defer res.Body.Close()
				body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
				if res.StatusCode >= 200 && res.StatusCode < 300 {
					var out map[string]any
					if json.Unmarshal(body, &out) == nil {
						oid := strings.TrimSpace(fmt.Sprint(out["external_order_id"]))
						if oid == "" {
							oid = strings.TrimSpace(fmt.Sprint(out["order_id"]))
						}
						if oid == "" {
							oid = strings.TrimSpace(fmt.Sprint(out["id"]))
						}
						if kid := strings.TrimSpace(fmt.Sprint(out["key_id"])); kid != "" && kid != "<nil>" {
							keyID = kid
						}
						if kid := strings.TrimSpace(fmt.Sprint(out["keyId"])); kid != "" && kid != "<nil>" {
							keyID = kid
						}
						if keyID == "" || keyID == "<nil>" {
							keyID = strings.TrimSpace(s.Cfg.RazorpayKeyID)
						}
						if oid != "" {
							return oid, keyID, amountINR, nil
						}
					}
				}
			}
		}
	}

	secret := strings.TrimSpace(s.Cfg.RazorpayKeySecret)
	if keyID != "" && secret != "" && !strings.Contains(keyID, "test_local") && paise > 0 {
		payload := map[string]any{
			"amount":   paise,
			"currency": "INR",
			"receipt":  receipt,
			"notes":    notes,
		}
		raw, _ := json.Marshal(payload)
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.razorpay.com/v1/orders", bytes.NewReader(raw))
		if reqErr == nil {
			req.SetBasicAuth(keyID, secret)
			req.Header.Set("Content-Type", "application/json")
			res, doErr := http.DefaultClient.Do(req)
			if doErr == nil {
				defer res.Body.Close()
				body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
				if res.StatusCode >= 200 && res.StatusCode < 300 {
					var out map[string]any
					if json.Unmarshal(body, &out) == nil {
						oid := strings.TrimSpace(fmt.Sprint(out["id"]))
						if oid != "" {
							return oid, keyID, amountINR, nil
						}
					}
				}
				return "", keyID, amountINR, fmt.Errorf("razorpay_http_%d", res.StatusCode)
			}
		}
	}

	// Dev fallback — Flutter may not open Razorpay Checkout with this id.
	return fmt.Sprintf("order_local_%s_%d", category, time.Now().Unix()), keyID, amountINR, nil
}
