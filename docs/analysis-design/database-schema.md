# 🗄️ Database Design: Relational Metadata Database (RMDS - PostgreSQL)

This document specifies the relational schema, entity relationships, integrity constraints, and state machine for the **Video Upload and VOD Pipeline** in **Bitmill**.

---

## 1. Entity-Relationship Diagram (ERD)

```mermaid
erDiagram
    VIDEOS ||--o{ UPLOAD_SESSIONS : "originates"
    VIDEOS ||--o{ VIDEO_RENDITIONS : "produces"
    VIDEOS ||--o{ PROCESSING_JOBS : "tracks"

    VIDEOS {
        uuid id PK "Primary identifier"
        uuid user_id FK "Owner account identifier"
        varchar title "Video display title"
        text description "Detailed description"
        varchar raw_bucket "Origin storage bucket for raw media"
        varchar raw_object_key "S3 object key of original file"
        bigint raw_size_bytes "Original file size in bytes"
        varchar mime_type "Media MIME type (e.g., video/mp4)"
        video_status status "Lifecycle state enum"
        numeric duration_seconds "Total media duration in seconds"
        varchar master_playlist_url "CDN URL to master.m3u8 playlist"
        varchar thumbnail_url "CDN URL to video poster thumbnail"
        timestamp created_at "Record creation timestamp"
        timestamp updated_at "Record last update timestamp"
    }

    UPLOAD_SESSIONS {
        uuid id PK "Session identifier"
        uuid video_id FK "Target video entity"
        text presigned_url "Signed upload URL issued to client"
        varchar upload_token "Security verification token"
        timestamp expires_at "Presigned URL expiration time"
        boolean is_consumed "Whether upload has been confirmed"
        varchar client_ip "Client IP address requesting ticket"
        timestamp created_at "Ticket issuance timestamp"
    }

    VIDEO_RENDITIONS {
        uuid id PK "Rendition identifier"
        uuid video_id FK "Parent video identifier"
        varchar resolution_name "1080p, 720p, 480p, 360p"
        integer width "Frame horizontal resolution (pixels)"
        integer height "Frame vertical resolution (pixels)"
        integer video_bitrate_kbps "Video stream bitrate in kbps"
        integer audio_bitrate_kbps "Audio stream bitrate in kbps"
        varchar playlist_path "Relative path to variant .m3u8"
        integer segment_count "Total count of .ts segments produced"
        rendition_status status "Rendition processing state"
        timestamp created_at "Completion timestamp"
    }

    PROCESSING_JOBS {
        uuid id PK "Job audit identifier"
        uuid video_id FK "Associated video"
        varchar worker_id "Worker instance identifier"
        job_status status "Current job state"
        integer attempt_count "Number of processing attempts"
        integer max_attempts "Configured retry threshold (default: 3)"
        text error_message "Diagnostic error trace if failed"
        timestamp started_at "Processing kickoff timestamp"
        timestamp finished_at "Processing completion timestamp"
        timestamp created_at "Job queue insertion timestamp"
    }
```

---

## 2. Video Lifecycle State Machine

Video entities transition through strict, deterministic state transitions:

```mermaid
stateDiagram-v2
    [*] --> PENDING_UPLOAD: Client requests presigned upload ticket
    PENDING_UPLOAD --> UPLOADED: Client confirms successful HTTP PUT to S3
    PENDING_UPLOAD --> FAILED: Upload window expired without confirmation
    
    UPLOADED --> PROCESSING: Worker consumes message from queue
    
    PROCESSING --> READY: FFmpeg transcode & multi-rendition HLS completed
    PROCESSING --> FAILED: Transcode crashed / Retries exhausted (Moved to DLQ)
    
    FAILED --> UPLOADED: Manual operator replay from DLQ
    READY --> [*]
```

### State Definitions:

| Status (`video_status`) | Description | Next Trigger / Action |
| :--- | :--- | :--- |
| `PENDING_UPLOAD` | Record created; Client holds a signed PUT URL. No media uploaded yet. | Awaits `/complete` confirmation from Client or S3 webhook. |
| `UPLOADED` | Source file exists in Raw Storage; processing job dispatched to Message Queue. | Worker pulls job from Queue to initiate transcoding. |
| `PROCESSING` | Worker is actively downloading, decoding, encoding ABR HLS, and generating thumbnails. | Worker finishes packaging and uploads HLS segments to Processed Storage. |
| `READY` | HLS master playlist and all renditions validated. Video is live on CDN. | Available for global streaming and seeking. |
| `FAILED` | Media corruption, unsupported codec, or worker timeout occurred. | Routed to Dead Letter Queue for engineering diagnostics. |

---

## 3. Production PostgreSQL DDL Script

Executable SQL script compatible with **PostgreSQL 14+**:

```sql
-- Enable UUID v4 generation extension
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- 1. ENUM Type Definitions
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

-- 2. Core VIDEOS Table
CREATE TABLE IF NOT EXISTS videos (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id UUID NOT NULL,
    title VARCHAR(255) NOT NULL,
    description TEXT,
    raw_bucket VARCHAR(128) NOT NULL,
    raw_object_key VARCHAR(512) NOT NULL,
    raw_size_bytes BIGINT DEFAULT 0,
    mime_type VARCHAR(64) NOT NULL DEFAULT 'video/mp4',
    status video_status NOT NULL DEFAULT 'PENDING_UPLOAD',
    duration_seconds NUMERIC(10, 2) DEFAULT 0.00,
    master_playlist_url VARCHAR(1024),
    thumbnail_url VARCHAR(1024),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Indices for performance querying
CREATE INDEX idx_videos_user_id ON videos(user_id);
CREATE INDEX idx_videos_status ON videos(status);
CREATE INDEX idx_videos_created_at ON videos(created_at DESC);

-- 3. UPLOAD_SESSIONS Table (Presigned ticket security & tracking)
CREATE TABLE IF NOT EXISTS upload_sessions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    video_id UUID NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    presigned_url TEXT NOT NULL,
    upload_token VARCHAR(128) NOT NULL UNIQUE,
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    is_consumed BOOLEAN NOT NULL DEFAULT FALSE,
    client_ip VARCHAR(45),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_upload_sessions_token ON upload_sessions(upload_token);
CREATE INDEX idx_upload_sessions_expires_at ON upload_sessions(expires_at);

-- 4. VIDEO_RENDITIONS Table (Adaptive Bitrate variants)
CREATE TABLE IF NOT EXISTS video_renditions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    video_id UUID NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    resolution_name VARCHAR(32) NOT NULL, -- '1080p', '720p', '480p', '360p'
    width INT NOT NULL,
    height INT NOT NULL,
    video_bitrate_kbps INT NOT NULL,
    audio_bitrate_kbps INT NOT NULL DEFAULT 128,
    playlist_path VARCHAR(512) NOT NULL,
    segment_count INT NOT NULL DEFAULT 0,
    status rendition_status NOT NULL DEFAULT 'PROCESSING',
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_video_resolution UNIQUE (video_id, resolution_name)
);

CREATE INDEX idx_video_renditions_video_id ON video_renditions(video_id);

-- 5. PROCESSING_JOBS Table (Worker execution audit & DLQ tracking)
CREATE TABLE IF NOT EXISTS processing_jobs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    video_id UUID NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    worker_id VARCHAR(64),
    status job_status NOT NULL DEFAULT 'QUEUED',
    attempt_count INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 3,
    error_message TEXT,
    started_at TIMESTAMP WITH TIME ZONE,
    finished_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_processing_jobs_video_id ON processing_jobs(video_id);
CREATE INDEX idx_processing_jobs_status ON processing_jobs(status);

-- 6. Trigger for Automatic updated_at Timestamp Updates
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trigger_videos_updated_at
BEFORE UPDATE ON videos
FOR EACH ROW
EXECUTE FUNCTION update_updated_at_column();
```
