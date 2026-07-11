package db

import (
	"time"

	"github.com/google/uuid"
)

// Variant status values stored in Postgres.
const (
	StatusPending = "pending"
	StatusReady   = "ready"
	StatusFailed  = "failed"
)

// Image is a row in the images table.
type Image struct {
	ID           uuid.UUID
	OriginalPath string
	ContentType  string
	SizeBytes    int64
	CreatedAt    time.Time
}

// Variant is a row in the variants table.
type Variant struct {
	ID         uuid.UUID
	ImageID    uuid.UUID
	ParamsHash string
	ParamsJSON []byte
	Status     string
	Path       *string
	Attempts   int
	LastError  *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
