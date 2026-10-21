package config

import (
	"os"
	"strings"
)

type Config struct {
	AppEnv                string
	HTTPAddr              string
	DBHost                string
	DBPort                string
	DBUser                string
	DBPassword            string
	DBName                string
	ServiceKey            string
	CORSOrigins           string
	JWTPublicKeyPath      string
	JWTIssuer             string
	JWTAudience           string
	DefaultProvider              string
	SubscriptionProvider         string
	RazorpayKeyID                string
	RazorpayKeySecret            string
	RazorpayWebhookSecret        string
	SubscriptionWebhookSecret    string
}

func FromEnv() Config {
	return Config{
		AppEnv:                getenv("APP_ENV", "local"),
		HTTPAddr:              getenv("HTTP_ADDR", ":8080"),
		DBHost:                getenv("DB_HOST", "postgres"),
		DBPort:                getenv("DB_PORT", "5432"),
		DBUser:                getenv("DB_USER", "identity"),
		DBPassword:            os.Getenv("DB_PASSWORD"),
		DBName:                getenv("DB_NAME", "fac_identity"),
		ServiceKey:            strings.TrimSpace(os.Getenv("PAYGATEWAY_SERVICE_KEY")),
		CORSOrigins:           getenv("CORS_ORIGINS", "*"),
		JWTPublicKeyPath:      getenv("JWT_PUBLIC_KEY_PATH", "/keys/jwt_public.pem"),
		JWTIssuer:             getenv("JWT_ISSUER", "https://api.facinect.local"),
		JWTAudience:           getenv("JWT_AUDIENCE", "facinect-apps"),
		DefaultProvider:           strings.ToLower(getenv("GATEWAY_DEFAULT_PROVIDER", "razorpay")),
		SubscriptionProvider:      strings.ToLower(getenv("SUBSCRIPTION_PROVIDER", "razorpay")),
		RazorpayKeyID:             strings.TrimSpace(os.Getenv("RAZORPAY_KEY_ID")),
		RazorpayKeySecret:         strings.TrimSpace(os.Getenv("RAZORPAY_KEY_SECRET")),
		RazorpayWebhookSecret:     strings.TrimSpace(os.Getenv("RAZORPAY_WEBHOOK_SECRET")),
		SubscriptionWebhookSecret: strings.TrimSpace(os.Getenv("RAZORPAY_SUBSCRIPTION_WEBHOOK_SECRET")),
	}
}

func (c Config) IsLocal() bool {
	return c.AppEnv == "local" || c.AppEnv == "development"
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
