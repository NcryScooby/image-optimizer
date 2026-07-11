package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool opens a pgx connection pool for the given Postgres URL.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}

const storePingTimeout = 2 * time.Second

// Ping verifies the pool can reach Postgres within a short timeout.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("db: store not connected")
	}
	pingCtx, cancel := context.WithTimeout(ctx, storePingTimeout)
	defer cancel()
	if err := s.pool.Ping(pingCtx); err != nil {
		return fmt.Errorf("db: ping: %w", err)
	}
	return nil
}
