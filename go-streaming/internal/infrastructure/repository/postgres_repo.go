package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"quidb.dev/go-streaming/internal/domain/video"
)

var (
	_ video.Repository              = (*PostgresRepo)(nil)
	_ video.UploadSessionRepository = (*PostgresRepo)(nil)
)

type PostgresRepo struct {
	pool *pgxpool.Pool
}

func NewPostgresRepo(pool *pgxpool.Pool) *PostgresRepo {
	return &PostgresRepo{pool: pool}
}

// ---------------------------------------------------------
// video.Repository Implementation
// ---------------------------------------------------------

func (r *PostgresRepo) Save(ctx context.Context, v *video.Video) error {
	query := `
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
	`

	_, err := r.pool.Exec(ctx, query,
		v.ID, v.UserID, v.Title, v.Description, v.RawBucket, v.RawObjectKey,
		v.RawSizeBytes, v.MimeType, string(v.Status), v.DurationSeconds,
		v.MasterPlaylistURL, v.ThumbnailURL, v.CreatedAt, v.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save video: %w", err)
	}

	return nil
}

func (r *PostgresRepo) FindByID(ctx context.Context, id string) (*video.Video, error) {
	query := `
		SELECT 
			id, user_id, title, description, raw_bucket, raw_object_key,
			raw_size_bytes, mime_type, status, duration_seconds,
			master_playlist_url, thumbnail_url, created_at, updated_at
		FROM videos
		WHERE id = $1;
	`

	row := r.pool.QueryRow(ctx, query, id)

	var v video.Video
	var statusStr string
	err := row.Scan(
		&v.ID, &v.UserID, &v.Title, &v.Description, &v.RawBucket, &v.RawObjectKey,
		&v.RawSizeBytes, &v.MimeType, &statusStr, &v.DurationSeconds,
		&v.MasterPlaylistURL, &v.ThumbnailURL, &v.CreatedAt, &v.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("video not found")
		}
		return nil, fmt.Errorf("failed to query video by id: %w", err)
	}

	v.Status = video.Status(statusStr)
	return &v, nil
}

func (r *PostgresRepo) List(ctx context.Context) ([]*video.Video, error) {
	query := `
		SELECT 
			id, user_id, title, description, raw_bucket, raw_object_key,
			raw_size_bytes, mime_type, status, duration_seconds,
			master_playlist_url, thumbnail_url, created_at, updated_at
		FROM videos
		ORDER BY created_at DESC;
	`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list videos: %w", err)
	}
	defer rows.Close()

	var result []*video.Video
	for rows.Next() {
		var v video.Video
		var statusStr string
		err := rows.Scan(
			&v.ID, &v.UserID, &v.Title, &v.Description, &v.RawBucket, &v.RawObjectKey,
			&v.RawSizeBytes, &v.MimeType, &statusStr, &v.DurationSeconds,
			&v.MasterPlaylistURL, &v.ThumbnailURL, &v.CreatedAt, &v.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan video row: %w", err)
		}
		v.Status = video.Status(statusStr)
		result = append(result, &v)
	}

	return result, nil
}

func (r *PostgresRepo) UpdateStatus(ctx context.Context, id string, status video.Status) error {
	query := `UPDATE videos SET status = $1, updated_at = NOW() WHERE id = $2;`
	_, err := r.pool.Exec(ctx, query, string(status), id)
	if err != nil {
		return fmt.Errorf("failed to update video status: %w", err)
	}
	return nil
}

func (r *PostgresRepo) UpdateStatusIf(ctx context.Context, id string, newStatus video.Status, currentStatus video.Status) (bool, error) {
	query := `
		UPDATE videos 
		SET status = $1, updated_at = NOW() 
		WHERE id = $2 AND status = $3;
	`
	tag, err := r.pool.Exec(ctx, query, string(newStatus), id, string(currentStatus))
	if err != nil {
		return false, fmt.Errorf("failed to update video status conditionally: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ---------------------------------------------------------
// video.UploadSessionRepository Implementation
// ---------------------------------------------------------

func (r *PostgresRepo) SaveSession(ctx context.Context, session *video.UploadSession) error {
	query := `
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
	`

	_, err := r.pool.Exec(ctx, query,
		session.ID, session.VideoID, session.UploadID, session.StorageBucket, session.StorageKey,
		session.FileFingerprint, session.TotalSizeBytes, session.PartSizeBytes, session.TotalParts,
		string(session.Status), session.ExpiresAt, session.CreatedAt, session.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save upload session: %w", err)
	}

	return nil
}

func (r *PostgresRepo) GetSessionByVideoID(ctx context.Context, videoID string) (*video.UploadSession, error) {
	query := `
		SELECT 
			id, video_id, upload_id, storage_bucket, storage_key,
			file_fingerprint, total_size_bytes, part_size_bytes, total_parts,
			status, expires_at, created_at, updated_at
		FROM upload_sessions
		WHERE video_id = $1
		ORDER BY created_at DESC
		LIMIT 1;
	`

	row := r.pool.QueryRow(ctx, query, videoID)

	var s video.UploadSession
	var statusStr string
	err := row.Scan(
		&s.ID, &s.VideoID, &s.UploadID, &s.StorageBucket, &s.StorageKey,
		&s.FileFingerprint, &s.TotalSizeBytes, &s.PartSizeBytes, &s.TotalParts,
		&statusStr, &s.ExpiresAt, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("upload session not found")
		}
		return nil, fmt.Errorf("failed to query upload session by video id: %w", err)
	}

	s.Status = video.UploadSessionStatus(statusStr)
	return &s, nil
}

func (r *PostgresRepo) GetSessionByFingerprint(ctx context.Context, fingerprint string) (*video.UploadSession, error) {
	query := `
		SELECT 
			id, video_id, upload_id, storage_bucket, storage_key,
			file_fingerprint, total_size_bytes, part_size_bytes, total_parts,
			status, expires_at, created_at, updated_at
		FROM upload_sessions
		WHERE file_fingerprint = $1 AND status = 'UPLOADING' AND expires_at > NOW()
		ORDER BY created_at DESC
		LIMIT 1;
	`

	row := r.pool.QueryRow(ctx, query, fingerprint)

	var s video.UploadSession
	var statusStr string
	err := row.Scan(
		&s.ID, &s.VideoID, &s.UploadID, &s.StorageBucket, &s.StorageKey,
		&s.FileFingerprint, &s.TotalSizeBytes, &s.PartSizeBytes, &s.TotalParts,
		&statusStr, &s.ExpiresAt, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // No active session
		}
		return nil, fmt.Errorf("failed to query upload session by fingerprint: %w", err)
	}

	s.Status = video.UploadSessionStatus(statusStr)
	return &s, nil
}

func (r *PostgresRepo) UpdateSessionStatus(ctx context.Context, sessionID string, status video.UploadSessionStatus) error {
	query := `UPDATE upload_sessions SET status = $1, updated_at = NOW() WHERE id = $2;`
	_, err := r.pool.Exec(ctx, query, string(status), sessionID)
	if err != nil {
		return fmt.Errorf("failed to update upload session status: %w", err)
	}
	return nil
}

func (r *PostgresRepo) RecordPartUploaded(ctx context.Context, sessionID string, partNumber int, etag string, sizeBytes int64) error {
	query := `
		INSERT INTO upload_parts (
			upload_session_id, part_number, etag, size_bytes, status, uploaded_at
		) VALUES (
			$1, $2, $3, $4, 'UPLOADED', NOW()
		)
		ON CONFLICT (upload_session_id, part_number) DO UPDATE SET
			etag = EXCLUDED.etag,
			size_bytes = EXCLUDED.size_bytes,
			status = 'UPLOADED',
			uploaded_at = NOW();
	`
	_, err := r.pool.Exec(ctx, query, sessionID, partNumber, etag, sizeBytes)
	if err != nil {
		return fmt.Errorf("failed to record uploaded part: %w", err)
	}
	return nil
}

func (r *PostgresRepo) GetCompletedParts(ctx context.Context, sessionID string) ([]video.CompletedPart, error) {
	query := `
		SELECT part_number, etag
		FROM upload_parts
		WHERE upload_session_id = $1 AND status = 'UPLOADED'
		ORDER BY part_number ASC;
	`
	rows, err := r.pool.Query(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get completed parts: %w", err)
	}
	defer rows.Close()

	var parts []video.CompletedPart
	for rows.Next() {
		var p video.CompletedPart
		if err := rows.Scan(&p.PartNumber, &p.ETag); err != nil {
			return nil, fmt.Errorf("failed to scan completed part: %w", err)
		}
		parts = append(parts, p)
	}
	return parts, nil
}

func (r *PostgresRepo) ListExpiredSessions(ctx context.Context, olderThan time.Duration) ([]*video.UploadSession, error) {
	threshold := time.Now().Add(-olderThan)
	query := `
		SELECT 
			id, video_id, upload_id, storage_bucket, storage_key,
			file_fingerprint, total_size_bytes, part_size_bytes, total_parts,
			status, expires_at, created_at, updated_at
		FROM upload_sessions
		WHERE status = 'UPLOADING' AND expires_at < $1
		ORDER BY expires_at ASC
		LIMIT 100;
	`
	rows, err := r.pool.Query(ctx, query, threshold)
	if err != nil {
		return nil, fmt.Errorf("failed to list expired sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*video.UploadSession
	for rows.Next() {
		var s video.UploadSession
		var statusStr string
		err := rows.Scan(
			&s.ID, &s.VideoID, &s.UploadID, &s.StorageBucket, &s.StorageKey,
			&s.FileFingerprint, &s.TotalSizeBytes, &s.PartSizeBytes, &s.TotalParts,
			&statusStr, &s.ExpiresAt, &s.CreatedAt, &s.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan expired session: %w", err)
		}
		s.Status = video.UploadSessionStatus(statusStr)
		sessions = append(sessions, &s)
	}
	return sessions, nil
}
