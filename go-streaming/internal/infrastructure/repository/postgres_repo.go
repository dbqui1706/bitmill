package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"quidb.dev/go-streaming/internal/domain/video"
	"quidb.dev/go-streaming/internal/infrastructure/repository/sqlc"
)

var (
	_ video.Repository              = (*PostgresRepo)(nil)
	_ video.UploadSessionRepository = (*PostgresRepo)(nil)
)

type PostgresRepo struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
}

func NewPostgresRepo(pool *pgxpool.Pool) *PostgresRepo {
	return &PostgresRepo{
		pool:    pool,
		queries: sqlc.New(pool),
	}
}

// ---------------------------------------------------------
// Helper Conversion Functions (UUID & Numeric)
// ---------------------------------------------------------

func toPgUUID(s string) pgtype.UUID {
	parsed, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{Valid: false}
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}
}

func fromPgUUID(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

func toPgText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

func fromPgText(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

func toPgNumeric(f float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(fmt.Sprintf("%.2f", f))
	return n
}

func fromPgNumeric(n pgtype.Numeric) float64 {
	val, err := n.Float64Value()
	if err != nil || !val.Valid {
		return 0.0
	}
	return val.Float64
}

func toPgTimestamptz(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{Valid: false}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// ---------------------------------------------------------
// video.Repository Implementation via SQLC
// ---------------------------------------------------------

func (r *PostgresRepo) Save(ctx context.Context, v *video.Video) error {
	params := sqlc.CreateOrUpdateVideoParams{
		ID:                toPgUUID(v.ID),
		UserID:            toPgUUID(v.UserID),
		Title:             v.Title,
		Description:       toPgText(v.Description),
		RawBucket:         v.RawBucket,
		RawObjectKey:      v.RawObjectKey,
		RawSizeBytes:      v.RawSizeBytes,
		MimeType:          v.MimeType,
		Status:            sqlc.VideoStatus(v.Status),
		DurationSeconds:   toPgNumeric(v.DurationSeconds),
		MasterPlaylistUrl: toPgText(v.MasterPlaylistURL),
		ThumbnailUrl:      toPgText(v.ThumbnailURL),
		CreatedAt:         toPgTimestamptz(v.CreatedAt),
		UpdatedAt:         toPgTimestamptz(v.UpdatedAt),
	}

	if err := r.queries.CreateOrUpdateVideo(ctx, params); err != nil {
		return fmt.Errorf("failed to save video via sqlc: %w", err)
	}

	return nil
}

func (r *PostgresRepo) FindByID(ctx context.Context, id string) (*video.Video, error) {
	row, err := r.queries.GetVideoByID(ctx, toPgUUID(id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("video not found")
		}
		return nil, fmt.Errorf("failed to query video by id via sqlc: %w", err)
	}

	return &video.Video{
		ID:                fromPgUUID(row.ID),
		UserID:            fromPgUUID(row.UserID),
		Title:             row.Title,
		Description:       fromPgText(row.Description),
		RawBucket:         row.RawBucket,
		RawObjectKey:      row.RawObjectKey,
		RawSizeBytes:      row.RawSizeBytes,
		MimeType:          row.MimeType,
		Status:            video.Status(row.Status),
		DurationSeconds:   fromPgNumeric(row.DurationSeconds),
		MasterPlaylistURL: fromPgText(row.MasterPlaylistUrl),
		ThumbnailURL:      fromPgText(row.ThumbnailUrl),
		CreatedAt:         row.CreatedAt.Time,
		UpdatedAt:         row.UpdatedAt.Time,
	}, nil
}

func (r *PostgresRepo) List(ctx context.Context) ([]*video.Video, error) {
	rows, err := r.queries.ListVideos(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list videos via sqlc: %w", err)
	}

	result := make([]*video.Video, 0, len(rows))
	for _, row := range rows {
		result = append(result, &video.Video{
			ID:                fromPgUUID(row.ID),
			UserID:            fromPgUUID(row.UserID),
			Title:             row.Title,
			Description:       fromPgText(row.Description),
			RawBucket:         row.RawBucket,
			RawObjectKey:      row.RawObjectKey,
			RawSizeBytes:      row.RawSizeBytes,
			MimeType:          row.MimeType,
			Status:            video.Status(row.Status),
			DurationSeconds:   fromPgNumeric(row.DurationSeconds),
			MasterPlaylistURL: fromPgText(row.MasterPlaylistUrl),
			ThumbnailURL:      fromPgText(row.ThumbnailUrl),
			CreatedAt:         row.CreatedAt.Time,
			UpdatedAt:         row.UpdatedAt.Time,
		})
	}

	return result, nil
}

func (r *PostgresRepo) UpdateStatus(ctx context.Context, id string, status video.Status) error {
	params := sqlc.UpdateVideoStatusParams{
		Status: sqlc.VideoStatus(status),
		ID:     toPgUUID(id),
	}
	if err := r.queries.UpdateVideoStatus(ctx, params); err != nil {
		return fmt.Errorf("failed to update video status via sqlc: %w", err)
	}
	return nil
}

func (r *PostgresRepo) UpdateStatusIf(ctx context.Context, id string, newStatus video.Status, currentStatus video.Status) (bool, error) {
	params := sqlc.UpdateVideoStatusIfParams{
		Status:   sqlc.VideoStatus(newStatus),
		ID:       toPgUUID(id),
		Status_2: sqlc.VideoStatus(currentStatus),
	}
	rowsAffected, err := r.queries.UpdateVideoStatusIf(ctx, params)
	if err != nil {
		return false, fmt.Errorf("failed to update video status conditionally via sqlc: %w", err)
	}
	return rowsAffected > 0, nil
}

// ---------------------------------------------------------
// video.UploadSessionRepository Implementation via SQLC
// ---------------------------------------------------------

func (r *PostgresRepo) SaveSession(ctx context.Context, session *video.UploadSession) error {
	params := sqlc.CreateOrUpdateUploadSessionParams{
		ID:               toPgUUID(session.ID),
		VideoID:          toPgUUID(session.VideoID),
		UploadID:         session.UploadID,
		StorageBucket:    session.StorageBucket,
		StorageKey:       session.StorageKey,
		FileFingerprint:  toPgText(session.FileFingerprint),
		TotalSizeBytes:   session.TotalSizeBytes,
		PartSizeBytes:    session.PartSizeBytes,
		TotalParts:       int32(session.TotalParts),
		Status:           string(session.Status),
		ExpiresAt:        toPgTimestamptz(session.ExpiresAt),
		CreatedAt:        toPgTimestamptz(session.CreatedAt),
		UpdatedAt:        toPgTimestamptz(session.UpdatedAt),
	}

	if err := r.queries.CreateOrUpdateUploadSession(ctx, params); err != nil {
		return fmt.Errorf("failed to save upload session via sqlc: %w", err)
	}

	return nil
}

func (r *PostgresRepo) GetSessionByVideoID(ctx context.Context, videoID string) (*video.UploadSession, error) {
	row, err := r.queries.GetUploadSessionByVideoID(ctx, toPgUUID(videoID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("upload session not found")
		}
		return nil, fmt.Errorf("failed to query upload session by video id via sqlc: %w", err)
	}

	return &video.UploadSession{
		ID:              fromPgUUID(row.ID),
		VideoID:         fromPgUUID(row.VideoID),
		UploadID:        row.UploadID,
		StorageBucket:   row.StorageBucket,
		StorageKey:      row.StorageKey,
		FileFingerprint: fromPgText(row.FileFingerprint),
		TotalSizeBytes:  row.TotalSizeBytes,
		PartSizeBytes:   row.PartSizeBytes,
		TotalParts:      int(row.TotalParts),
		Status:          video.UploadSessionStatus(row.Status),
		ExpiresAt:       row.ExpiresAt.Time,
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
	}, nil
}

func (r *PostgresRepo) GetSessionByFingerprint(ctx context.Context, fingerprint string) (*video.UploadSession, error) {
	row, err := r.queries.GetActiveUploadSessionByFingerprint(ctx, toPgText(fingerprint))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // No active session found
		}
		return nil, fmt.Errorf("failed to query active session by fingerprint via sqlc: %w", err)
	}

	return &video.UploadSession{
		ID:              fromPgUUID(row.ID),
		VideoID:         fromPgUUID(row.VideoID),
		UploadID:        row.UploadID,
		StorageBucket:   row.StorageBucket,
		StorageKey:      row.StorageKey,
		FileFingerprint: fromPgText(row.FileFingerprint),
		TotalSizeBytes:  row.TotalSizeBytes,
		PartSizeBytes:   row.PartSizeBytes,
		TotalParts:      int(row.TotalParts),
		Status:          video.UploadSessionStatus(row.Status),
		ExpiresAt:       row.ExpiresAt.Time,
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
	}, nil
}

func (r *PostgresRepo) UpdateSessionStatus(ctx context.Context, sessionID string, status video.UploadSessionStatus) error {
	params := sqlc.UpdateUploadSessionStatusParams{
		Status: string(status),
		ID:     toPgUUID(sessionID),
	}
	if err := r.queries.UpdateUploadSessionStatus(ctx, params); err != nil {
		return fmt.Errorf("failed to update upload session status via sqlc: %w", err)
	}
	return nil
}

func (r *PostgresRepo) RecordPartUploaded(ctx context.Context, sessionID string, partNumber int, etag string, sizeBytes int64) error {
	params := sqlc.RecordPartUploadedParams{
		ID:                toPgUUID(uuid.NewString()),
		UploadSessionID:   toPgUUID(sessionID),
		PartNumber:        int32(partNumber),
		Etag:              toPgText(etag),
		SizeBytes:         sizeBytes,
	}

	if err := r.queries.RecordPartUploaded(ctx, params); err != nil {
		return fmt.Errorf("failed to record uploaded part via sqlc: %w", err)
	}
	return nil
}

func (r *PostgresRepo) GetCompletedParts(ctx context.Context, sessionID string) ([]video.CompletedPart, error) {
	rows, err := r.queries.GetCompletedPartsBySessionID(ctx, toPgUUID(sessionID))
	if err != nil {
		return nil, fmt.Errorf("failed to get completed parts via sqlc: %w", err)
	}

	parts := make([]video.CompletedPart, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, video.CompletedPart{
			PartNumber: int(row.PartNumber),
			ETag:       fromPgText(row.Etag),
		})
	}
	return parts, nil
}

func (r *PostgresRepo) ListExpiredSessions(ctx context.Context, olderThan time.Duration) ([]*video.UploadSession, error) {
	threshold := toPgTimestamptz(time.Now().Add(-olderThan))
	rows, err := r.queries.ListExpiredUploadSessions(ctx, threshold)
	if err != nil {
		return nil, fmt.Errorf("failed to list expired sessions via sqlc: %w", err)
	}

	sessions := make([]*video.UploadSession, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, &video.UploadSession{
			ID:              fromPgUUID(row.ID),
			VideoID:         fromPgUUID(row.VideoID),
			UploadID:        row.UploadID,
			StorageBucket:   row.StorageBucket,
			StorageKey:      row.StorageKey,
			FileFingerprint: fromPgText(row.FileFingerprint),
			TotalSizeBytes:  row.TotalSizeBytes,
			PartSizeBytes:   row.PartSizeBytes,
			TotalParts:      int(row.TotalParts),
			Status:          video.UploadSessionStatus(row.Status),
			ExpiresAt:       row.ExpiresAt.Time,
			CreatedAt:       row.CreatedAt.Time,
			UpdatedAt:       row.UpdatedAt.Time,
		})
	}
	return sessions, nil
}
