package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func Open(host, port, user, password, name string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		user, password, host, port, name,
	)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func EnsureSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS gateway_orders (
  id                 BIGSERIAL PRIMARY KEY,
  provider           VARCHAR(32) NOT NULL,
  external_order_id  VARCHAR(128) NULL,
  amount_paise       BIGINT NOT NULL,
  currency           VARCHAR(8) NOT NULL DEFAULT 'INR',
  receipt            VARCHAR(128) NULL,
  status             VARCHAR(32) NOT NULL DEFAULT 'created'
                     CHECK (status IN ('created','paid','failed','refunded')),
  facility_id        BIGINT NULL,
  purpose            VARCHAR(64) NULL,
  metadata           JSONB NULL,
  checkout_json      JSONB NULL,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_gateway_orders_provider
  ON gateway_orders(provider, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_gateway_orders_external
  ON gateway_orders(provider, external_order_id);
CREATE INDEX IF NOT EXISTS idx_gateway_orders_receipt
  ON gateway_orders(receipt) WHERE receipt IS NOT NULL AND receipt <> '';
ALTER TABLE gateway_orders ADD COLUMN IF NOT EXISTS payment_id VARCHAR(128) NULL;
`)
	return err
}
