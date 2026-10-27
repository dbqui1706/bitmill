package video

import (
	"context"
	"time"
)

// Repository defines the storage contract for the Video aggregate.
type Repository interface {
	Save(ctx context.Context, v *Video) error
	FindByID(ctx context.Context, id string) (*Video, error)
	List(ctx context.Context) ([]*Video, error)
	UpdateStatus(ctx context.Context, id string, status Status) error
	// UpdateStatusIf performs an atomic conditional update, preventing race conditions (RFC-001)
	UpdateStatusIf(ctx context.Context, id string, newStatus Status, currentStatus Status) (bool, error)
}

// UploadSessionRepository defines the storage contract for multipart upload sessions.
type UploadSessionRepository interface {
	SaveSession(ctx context.Context, session *UploadSession) error
	GetSessionByVideoID(ctx context.Context, videoID string) (*UploadSession, error)
	GetSessionByFingerprint(ctx context.Context, fingerprint string) (*UploadSession, error)
	UpdateSessionStatus(ctx context.Context, sessionID string, status UploadSessionStatus) error
	RecordPartUploaded(ctx context.Context, sessionID string, partNumber int, etag string, sizeBytes int64) error
	GetCompletedParts(ctx context.Context, sessionID string) ([]CompletedPart, error)
	ListExpiredSessions(ctx context.Context, olderThan time.Duration) ([]*UploadSession, error)
}

// Transcoder defines the media transformation engine contract (Docker FFmpeg).
type Transcoder interface {
	TranscodeToHLS(ctx context.Context, sourcePath, outputDir, playlistName string) error
}
