package config

import (
	"os"
	"strings"
)

type Config struct {
	AppEnv           string
	HTTPAddr         string
	DBHost           string
	DBPort           string
	DBUser           string
	DBPassword       string
	DBName           string
	ServiceKey       string
	CORSOrigins      string
	JWTPublicKeyPath string
	JWTIssuer        string
	JWTAudience      string
	RazorpayKeyID      string
	RazorpayKeySecret  string
	PaygatewayURL      string
	PaygatewayKey      string
	NotificationsURL              string
	NotificationsKey              string
	BookingCancelWhatsAppTemplate string
}

func FromEnv() Config {
	return Config{
		AppEnv:                        getenv("APP_ENV", "local"),
		HTTPAddr:                      getenv("HTTP_ADDR", ":8080"),
		DBHost:                        getenv("DB_HOST", "postgres"),
		DBPort:                        getenv("DB_PORT", "5432"),
		DBUser:                        getenv("DB_USER", "identity"),
		DBPassword:                    os.Getenv("DB_PASSWORD"),
		DBName:                        getenv("DB_NAME", "fac_identity"),
		ServiceKey:                    strings.TrimSpace(os.Getenv("APP_SERVICE_KEY")),
		CORSOrigins:                   getenv("CORS_ORIGINS", "*"),
		JWTPublicKeyPath:              getenv("JWT_PUBLIC_KEY_PATH", "/keys/jwt_public.pem"),
		JWTIssuer:                     getenv("JWT_ISSUER", "https://api.facinect.local"),
		JWTAudience:                   getenv("JWT_AUDIENCE", "facinect-apps"),
		RazorpayKeyID:                 getenv("RAZORPAY_KEY_ID", "rzp_test_local"),
		RazorpayKeySecret:             strings.TrimSpace(os.Getenv("RAZORPAY_KEY_SECRET")),
		PaygatewayURL:                 strings.TrimRight(getenv("PAYGATEWAY_URL", "http://paygateway:8080"), "/"),
		PaygatewayKey:                 strings.TrimSpace(os.Getenv("PAYGATEWAY_SERVICE_KEY")),
		NotificationsURL:              strings.TrimRight(getenv("NOTIFICATIONS_BASE_URL", "http://notifications:8080"), "/"),
		NotificationsKey:              strings.TrimSpace(os.Getenv("NOTIFICATIONS_SERVICE_KEY")),
		// No cancel template in PHP catalog — create in Meta or override.
		BookingCancelWhatsAppTemplate: getenv("BOOKING_CANCEL_WHATSAPP_TEMPLATE", "booking_cancellation"),
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
