-- Bitmill PostgreSQL 15+ Unified Schema for SQLC

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TYPE video_status AS ENUM (
    'PENDING_UPLOAD',
    'UPLOADED',
    'PROCESSING',
    'READY',
    'FAILED'
);

CREATE TYPE rendition_status AS ENUM (
    'PROCESSING',
    'READY',
    'FAILED'
);

CREATE TYPE job_status AS ENUM (
    'QUEUED',
    'PROCESSING',
    'COMPLETED',
    'FAILED',
    'DLQ'
);

CREATE TABLE videos (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    title VARCHAR(255) NOT NULL,
    description TEXT,
    raw_bucket VARCHAR(128) NOT NULL,
    raw_object_key VARCHAR(512) NOT NULL,
    raw_size_bytes BIGINT NOT NULL DEFAULT 0,
    mime_type VARCHAR(64) NOT NULL DEFAULT 'video/mp4',
    status video_status NOT NULL DEFAULT 'PENDING_UPLOAD',
    duration_seconds NUMERIC(10, 2) NOT NULL DEFAULT 0.00,
    master_playlist_url VARCHAR(1024),
    thumbnail_url VARCHAR(1024),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE upload_sessions (
    id UUID PRIMARY KEY,
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

CREATE TABLE upload_parts (
    id UUID PRIMARY KEY,
    upload_session_id UUID NOT NULL REFERENCES upload_sessions(id) ON DELETE CASCADE,
    part_number INT NOT NULL,
    etag VARCHAR(128),
    size_bytes BIGINT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'PENDING',
    uploaded_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_session_part UNIQUE (upload_session_id, part_number)
);

CREATE TABLE video_renditions (
    id UUID PRIMARY KEY,
    video_id UUID NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    resolution_name VARCHAR(32) NOT NULL,
    width INT NOT NULL,
    height INT NOT NULL,
    video_bitrate_kbps INT NOT NULL,
    audio_bitrate_kbps INT NOT NULL DEFAULT 128,
    playlist_path VARCHAR(512) NOT NULL,
    segment_count INT NOT NULL DEFAULT 0,
    status rendition_status NOT NULL DEFAULT 'PROCESSING',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_video_resolution UNIQUE (video_id, resolution_name)
);

CREATE TABLE processing_jobs (
    id UUID PRIMARY KEY,
    video_id UUID NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    worker_id VARCHAR(64),
    status job_status NOT NULL DEFAULT 'QUEUED',
    attempt_count INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 3,
    error_message TEXT,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
