package db

import (
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Migrate applies pending goose migrations from the default "migrations" directory
// (repo root locally; /app/migrations in the Docker image). Used on serve boot.
func Migrate(databaseURL string) error {
	return MigrateDir(databaseURL, "migrations")
}

// MigrateDir applies goose migrations from dir (filesystem path relative to CWD
// or absolute).
func MigrateDir(databaseURL, dir string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open db for migrate: %w", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return fmt.Errorf("ping db for migrate: %w", err)
	}

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}

	if err := goose.Up(db, dir); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}
