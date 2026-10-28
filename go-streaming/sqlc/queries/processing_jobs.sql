-- name: CreateProcessingJob :one
INSERT INTO processing_jobs (
    video_id, worker_id, status, attempt_count, max_attempts
) VALUES (
    $1, $2, 'QUEUED', 0, $3
) RETURNING *;

-- name: UpdateJobProcessing :exec
UPDATE processing_jobs
SET status = 'PROCESSING',
    worker_id = $2,
    attempt_count = attempt_count + 1,
    started_at = NOW()
WHERE id = $1;

-- name: UpdateJobCompleted :exec
UPDATE processing_jobs
SET status = 'COMPLETED',
    finished_at = NOW()
WHERE id = $1;

-- name: UpdateJobFailed :exec
UPDATE processing_jobs
SET status = 'FAILED',
    error_message = $2,
    finished_at = NOW()
WHERE id = $1;

-- name: UpdateJobDLQ :exec
UPDATE processing_jobs
SET status = 'DLQ',
    error_message = $2,
    finished_at = NOW()
WHERE id = $1;

-- name: GetActiveJobByVideoID :one
SELECT * FROM processing_jobs
WHERE video_id = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: GetJobByID :one
SELECT * FROM processing_jobs
WHERE id = $1;
