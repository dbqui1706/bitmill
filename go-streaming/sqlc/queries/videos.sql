-- name: CreateOrUpdateVideo :exec
INSERT INTO videos (
    id, user_id, title, description, raw_bucket, raw_object_key,
    raw_size_bytes, mime_type, status, duration_seconds,
    master_playlist_url, thumbnail_url, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10,
    $11, $12, $13, $14
)
ON CONFLICT (id) DO UPDATE SET
    title = EXCLUDED.title,
    description = EXCLUDED.description,
    raw_size_bytes = EXCLUDED.raw_size_bytes,
    status = EXCLUDED.status,
    duration_seconds = EXCLUDED.duration_seconds,
    master_playlist_url = EXCLUDED.master_playlist_url,
    thumbnail_url = EXCLUDED.thumbnail_url,
    updated_at = NOW();

-- name: GetVideoByID :one
SELECT 
    id, user_id, title, description, raw_bucket, raw_object_key,
    raw_size_bytes, mime_type, status, duration_seconds,
    master_playlist_url, thumbnail_url, created_at, updated_at
FROM videos
WHERE id = $1;

-- name: ListVideos :many
SELECT 
    id, user_id, title, description, raw_bucket, raw_object_key,
    raw_size_bytes, mime_type, status, duration_seconds,
    master_playlist_url, thumbnail_url, created_at, updated_at
FROM videos
ORDER BY created_at DESC;

-- name: UpdateVideoStatus :exec
UPDATE videos 
SET status = $1, updated_at = NOW() 
WHERE id = $2;

-- name: UpdateVideoStatusIf :execrows
UPDATE videos 
SET status = $1, updated_at = NOW() 
WHERE id = $2 AND status = $3;
