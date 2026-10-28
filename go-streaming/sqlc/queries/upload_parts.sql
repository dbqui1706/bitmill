-- name: RecordPartUploaded :exec
INSERT INTO upload_parts (
    id, upload_session_id, part_number, etag, size_bytes, status, uploaded_at, created_at
) VALUES (
    $1, $2, $3, $4, $5, 'UPLOADED', NOW(), NOW()
)
ON CONFLICT (upload_session_id, part_number) DO UPDATE SET
    etag = EXCLUDED.etag,
    size_bytes = EXCLUDED.size_bytes,
    status = 'UPLOADED',
    uploaded_at = NOW();

-- name: GetCompletedPartsBySessionID :many
SELECT part_number, etag
FROM upload_parts
WHERE upload_session_id = $1 AND status = 'UPLOADED'
ORDER BY part_number ASC;
