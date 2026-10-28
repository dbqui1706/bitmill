-- name: UpsertRendition :one
INSERT INTO video_renditions (
    video_id, resolution_name, width, height,
    video_bitrate_kbps, audio_bitrate_kbps,
    playlist_path, segment_count, status
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
ON CONFLICT (video_id, resolution_name) DO UPDATE SET
    width = EXCLUDED.width,
    height = EXCLUDED.height,
    video_bitrate_kbps = EXCLUDED.video_bitrate_kbps,
    audio_bitrate_kbps = EXCLUDED.audio_bitrate_kbps,
    playlist_path = EXCLUDED.playlist_path,
    segment_count = EXCLUDED.segment_count,
    status = EXCLUDED.status
RETURNING *;

-- name: ListRenditionsByVideoID :many
SELECT * FROM video_renditions
WHERE video_id = $1
ORDER BY height DESC;
