package main

import (
	"log"
	"net/http"

	"github.com/facinect/members/internal/authn"
	"github.com/facinect/members/internal/config"
	"github.com/facinect/members/internal/db"
	"github.com/facinect/members/internal/httpapi"
	"github.com/facinect/members/internal/members"
)

func main() {
	cfg := config.FromEnv()
	sqlDB, err := db.Open(cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer sqlDB.Close()
	if err := db.EnsureSchema(sqlDB); err != nil {
		log.Fatalf("schema: %v", err)
	}

	var jwtVerifier *authn.Verifier
	jv, err := authn.NewVerifier(cfg.JWTPublicKeyPath, cfg.JWTIssuer, cfg.JWTAudience)
	if err != nil {
		if cfg.IsLocal() {
			log.Printf("jwt verifier unavailable (local): %v", err)
		} else {
			log.Fatalf("jwt: %v", err)
		}
	} else {
		jwtVerifier = jv
	}

	svc := &members.Service{DB: sqlDB, Cfg: cfg}
	svc.StartPaymentReminderCron()
	srv := &httpapi.Server{Cfg: cfg, Members: svc, JWT: jwtVerifier}
	log.Printf("members (go) listening on %s service_key=%v jwt=%v",
		cfg.HTTPAddr, cfg.ServiceKey != "", jwtVerifier != nil)
	if err := http.ListenAndServe(cfg.HTTPAddr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
