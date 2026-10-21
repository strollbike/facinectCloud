package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/facinect/students/internal/authn"
	"github.com/facinect/students/internal/config"
	"github.com/facinect/students/internal/db"
	"github.com/facinect/students/internal/httpapi"
	"github.com/facinect/students/internal/students"
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

	svc := &students.Service{DB: sqlDB, Cfg: cfg}
	syncCtx, syncCancel := context.WithTimeout(context.Background(), 45*time.Second)
	if err := svc.SyncAllCoachingBlocks(syncCtx); err != nil {
		log.Printf("coaching active blocks: %v", err)
	}
	syncCancel()
	srv := &httpapi.Server{Cfg: cfg, Students: svc, JWT: jwtVerifier}
	log.Printf("students (go) listening on %s service_key=%v jwt=%v",
		cfg.HTTPAddr, cfg.ServiceKey != "", jwtVerifier != nil)
	if err := http.ListenAndServe(cfg.HTTPAddr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
