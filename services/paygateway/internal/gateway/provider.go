package gateway

import (
	"context"
	"net/http"
)

// Provider is the integration abstraction for third-party payment gateways.
type Provider interface {
	Name() string
	Configured() bool
	CreateOrder(ctx context.Context, req CreateRequest) (*CreateResult, error)
	CreatePaymentLink(ctx context.Context, req PaymentLinkRequest) (*PaymentLinkResult, error)
	CancelPaymentLink(ctx context.Context, linkID string) error
	Verify(ctx context.Context, req VerifyRequest) (*VerifyResult, error)
	ParseWebhook(headers http.Header, body []byte) (*NormalizedEvent, error)
}
