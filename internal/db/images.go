package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store provides image and variant persistence against Postgres.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a pgx pool with repository methods.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// ErrNotFound is returned when an image or variant row does not exist.
var ErrNotFound = errors.New("not found")

func scanImage(row pgx.Row) (Image, error) {
	var img Image
	err := row.Scan(
		&img.ID,
		&img.OriginalPath,
		&img.ContentType,
		&img.SizeBytes,
		&img.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Image{}, ErrNotFound
	}
	if err != nil {
		return Image{}, err
	}
	return img, nil
}

func scanVariant(row pgx.Row) (Variant, error) {
	var v Variant
	err := row.Scan(
		&v.ID,
		&v.ImageID,
		&v.ParamsHash,
		&v.ParamsJSON,
		&v.Status,
		&v.Path,
		&v.Attempts,
		&v.LastError,
		&v.CreatedAt,
		&v.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Variant{}, ErrNotFound
	}
	if err != nil {
		return Variant{}, err
	}
	return v, nil
}

const imageColumns = `id, original_path, content_type, size_bytes, created_at`
const variantColumns = `id, image_id, params_hash, params_json, status, path, attempts, last_error, created_at, updated_at`

// InsertImage inserts a new images row.
func (s *Store) InsertImage(ctx context.Context, id uuid.UUID, originalPath, contentType string, sizeBytes int64) (Image, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO images (id, original_path, content_type, size_bytes)
		VALUES ($1, $2, $3, $4)
		RETURNING `+imageColumns+`
	`, id, originalPath, contentType, sizeBytes)

	img, err := scanImage(row)
	if err != nil {
		return Image{}, fmt.Errorf("insert image: %w", err)
	}
	return img, nil
}

// GetImage loads an image by id.
func (s *Store) GetImage(ctx context.Context, id uuid.UUID) (Image, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+imageColumns+`
		FROM images
		WHERE id = $1
	`, id)

	img, err := scanImage(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Image{}, ErrNotFound
		}
		return Image{}, fmt.Errorf("get image: %w", err)
	}
	return img, nil
}

// DeleteImage deletes an image (variants cascade via FK).
func (s *Store) DeleteImage(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM images WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete image: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
