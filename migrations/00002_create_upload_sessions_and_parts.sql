-- +goose Up
-- +goose StatementBegin
CREATE TABLE upload_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    video_id UUID NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    upload_id TEXT NOT NULL,
    storage_bucket VARCHAR(128) NOT NULL,
    storage_key TEXT NOT NULL,
    file_fingerprint VARCHAR(128),
    total_size_bytes BIGINT NOT NULL DEFAULT 0,
    part_size_bytes BIGINT NOT NULL DEFAULT 20971520,
    total_parts INT NOT NULL DEFAULT 1,
    status VARCHAR(32) NOT NULL DEFAULT 'UPLOADING',
    expires_at TIMESTAMPTZ NOT NULL,
    is_consumed BOOLEAN NOT NULL DEFAULT FALSE,
    client_ip VARCHAR(45),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_upload_sessions_video_id ON upload_sessions(video_id);
CREATE INDEX idx_upload_sessions_upload_id ON upload_sessions(upload_id);
CREATE INDEX idx_upload_sessions_status ON upload_sessions(status);
CREATE INDEX idx_upload_sessions_fingerprint ON upload_sessions(file_fingerprint);
CREATE INDEX idx_upload_sessions_expires_at ON upload_sessions(expires_at);

CREATE TRIGGER trg_upload_sessions_updated_at
BEFORE UPDATE ON upload_sessions
FOR EACH ROW
EXECUTE FUNCTION update_updated_at_column();

CREATE TABLE upload_parts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    upload_session_id UUID NOT NULL REFERENCES upload_sessions(id) ON DELETE CASCADE,
    part_number INT NOT NULL CHECK (part_number BETWEEN 1 AND 10000),
    etag VARCHAR(128),
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    status VARCHAR(32) NOT NULL DEFAULT 'PENDING',
    uploaded_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_session_part UNIQUE (upload_session_id, part_number)
);

CREATE INDEX idx_upload_parts_session_status ON upload_parts(upload_session_id, status);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS upload_parts CASCADE;
DROP TABLE IF EXISTS upload_sessions CASCADE;
-- +goose StatementEnd
