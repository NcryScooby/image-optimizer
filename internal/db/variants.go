package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// UpsertPendingVariant inserts a pending variant when none exists for
// (image_id, params_hash). On unique conflict it returns the existing row
// without overwriting a ready (or any other) status.
// created is true only when this call inserted the row (caller should publish).
func (s *Store) UpsertPendingVariant(ctx context.Context, imageID uuid.UUID, paramsHash string, paramsJSON []byte) (v Variant, created bool, err error) {
	id := uuid.New()
	row := s.pool.QueryRow(ctx, `
		INSERT INTO variants (id, image_id, params_hash, params_json, status)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (image_id, params_hash) DO NOTHING
		RETURNING `+variantColumns+`
	`, id, imageID, paramsHash, paramsJSON, StatusPending)

	v, err = scanVariant(row)
	if err == nil {
		return v, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Variant{}, false, fmt.Errorf("upsert pending variant: %w", err)
	}

	// Conflict: return existing row unchanged (do not overwrite ready).
	existing, err := s.GetVariantByHash(ctx, imageID, paramsHash)
	if err != nil {
		return Variant{}, false, fmt.Errorf("upsert pending variant conflict fetch: %w", err)
	}
	return existing, false, nil
}

// GetVariantByHash loads a variant by image id and params hash.
func (s *Store) GetVariantByHash(ctx context.Context, imageID uuid.UUID, paramsHash string) (Variant, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+variantColumns+`
		FROM variants
		WHERE image_id = $1 AND params_hash = $2
	`, imageID, paramsHash)

	v, err := scanVariant(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Variant{}, ErrNotFound
		}
		return Variant{}, fmt.Errorf("get variant by hash: %w", err)
	}
	return v, nil
}

// GetVariantByID loads a variant by primary key.
func (s *Store) GetVariantByID(ctx context.Context, id uuid.UUID) (Variant, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+variantColumns+`
		FROM variants
		WHERE id = $1
	`, id)

	v, err := scanVariant(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Variant{}, ErrNotFound
		}
		return Variant{}, fmt.Errorf("get variant by id: %w", err)
	}
	return v, nil
}

// MarkReady sets status=ready, path, clears last_error, and bumps updated_at.
func (s *Store) MarkReady(ctx context.Context, id uuid.UUID, path string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE variants
		SET status = $2,
		    path = $3,
		    last_error = NULL,
		    updated_at = now()
		WHERE id = $1
	`, id, StatusReady, path)
	if err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkFailed sets status=failed, last_error, and bumps updated_at.
func (s *Store) MarkFailed(ctx context.Context, id uuid.UUID, lastError string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE variants
		SET status = $2,
		    last_error = $3,
		    updated_at = now()
		WHERE id = $1
	`, id, StatusFailed, lastError)
	if err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePendingVariant removes a pending variant row so a later GET can
// UpsertPending+Publish again (e.g. after a failed queue publish).
func (s *Store) DeletePendingVariant(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM variants
		WHERE id = $1 AND status = $2
	`, id, StatusPending)
	if err != nil {
		return fmt.Errorf("delete pending variant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// IncrAttempts increments attempts and returns the new value.
func (s *Store) IncrAttempts(ctx context.Context, id uuid.UUID) (int, error) {
	var attempts int
	err := s.pool.QueryRow(ctx, `
		UPDATE variants
		SET attempts = attempts + 1,
		    updated_at = now()
		WHERE id = $1
		RETURNING attempts
	`, id).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("incr attempts: %w", err)
	}
	return attempts, nil
}
