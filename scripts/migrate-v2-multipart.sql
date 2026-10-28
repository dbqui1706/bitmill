-- Bitmill Migration V2: Multipart Resumable Upload Support
-- Conforms to RFC-001 specifications

-- 1. Alter upload_sessions table to support S3/R2 Multipart Upload
ALTER TABLE upload_sessions ALTER COLUMN presigned_url DROP NOT NULL;
ALTER TABLE upload_sessions ALTER COLUMN upload_token DROP NOT NULL;

ALTER TABLE upload_sessions 
    ADD COLUMN IF NOT EXISTS upload_id TEXT,
    ADD COLUMN IF NOT EXISTS storage_bucket VARCHAR(128),
    ADD COLUMN IF NOT EXISTS storage_key TEXT,
    ADD COLUMN IF NOT EXISTS file_fingerprint VARCHAR(128),
    ADD COLUMN IF NOT EXISTS total_size_bytes BIGINT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS part_size_bytes BIGINT DEFAULT 20971520,
    ADD COLUMN IF NOT EXISTS total_parts INT DEFAULT 1,
    ADD COLUMN IF NOT EXISTS status VARCHAR(32) NOT NULL DEFAULT 'UPLOADING',
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP;

CREATE INDEX IF NOT EXISTS idx_upload_sessions_upload_id ON upload_sessions(upload_id);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_status ON upload_sessions(status);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_fingerprint ON upload_sessions(file_fingerprint);

-- 2. Create upload_parts table
CREATE TABLE IF NOT EXISTS upload_parts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    upload_session_id UUID NOT NULL REFERENCES upload_sessions(id) ON DELETE CASCADE,
    part_number INT NOT NULL CHECK (part_number BETWEEN 1 AND 10000),
    etag VARCHAR(128),
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    status VARCHAR(32) NOT NULL DEFAULT 'PENDING',
    uploaded_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_session_part UNIQUE (upload_session_id, part_number)
);

CREATE INDEX IF NOT EXISTS idx_upload_parts_session_status ON upload_parts(upload_session_id, status);

-- 3. Trigger for updated_at on upload_sessions
CREATE OR REPLACE TRIGGER trg_upload_sessions_updated_at
BEFORE UPDATE ON upload_sessions
FOR EACH ROW
EXECUTE FUNCTION update_updated_at_column();
