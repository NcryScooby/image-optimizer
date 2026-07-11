-- +goose Up
-- +goose StatementBegin
CREATE TABLE images (
    id            UUID PRIMARY KEY,
    original_path TEXT        NOT NULL,
    content_type  TEXT        NOT NULL,
    size_bytes    BIGINT      NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE variants (
    id           UUID PRIMARY KEY,
    image_id     UUID        NOT NULL REFERENCES images (id) ON DELETE CASCADE,
    params_hash  TEXT        NOT NULL,
    params_json  JSONB       NOT NULL,
    status       TEXT        NOT NULL CHECK (status IN ('pending', 'ready', 'failed')),
    path         TEXT,
    attempts     INT         NOT NULL DEFAULT 0,
    last_error   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (image_id, params_hash)
);

CREATE INDEX variants_image_id_idx ON variants (image_id);
CREATE INDEX variants_status_idx ON variants (status);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS variants;
DROP TABLE IF EXISTS images;
-- +goose StatementEnd
