-- name: CreateOrUpdateUploadSession :exec
INSERT INTO upload_sessions (
    id, video_id, upload_id, storage_bucket, storage_key,
    file_fingerprint, total_size_bytes, part_size_bytes, total_parts,
    status, expires_at, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9,
    $10, $11, $12, $13
)
ON CONFLICT (id) DO UPDATE SET
    status = EXCLUDED.status,
    updated_at = NOW();

-- name: GetUploadSessionByVideoID :one
SELECT 
    id, video_id, upload_id, storage_bucket, storage_key,
    file_fingerprint, total_size_bytes, part_size_bytes, total_parts,
    status, expires_at, created_at, updated_at
FROM upload_sessions
WHERE video_id = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: GetActiveUploadSessionByFingerprint :one
SELECT 
    id, video_id, upload_id, storage_bucket, storage_key,
    file_fingerprint, total_size_bytes, part_size_bytes, total_parts,
    status, expires_at, created_at, updated_at
FROM upload_sessions
WHERE file_fingerprint = $1 AND status = 'UPLOADING' AND expires_at > NOW()
ORDER BY created_at DESC
LIMIT 1;

-- name: UpdateUploadSessionStatus :exec
UPDATE upload_sessions 
SET status = $1, updated_at = NOW() 
WHERE id = $2;

-- name: ListExpiredUploadSessions :many
SELECT 
    id, video_id, upload_id, storage_bucket, storage_key,
    file_fingerprint, total_size_bytes, part_size_bytes, total_parts,
    status, expires_at, created_at, updated_at
FROM upload_sessions
WHERE status = 'UPLOADING' AND expires_at < $1
ORDER BY expires_at ASC
LIMIT 100;
