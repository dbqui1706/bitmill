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
	MarkReady(ctx context.Context, id, masterPlaylistURL, thumbnailURL string, durationSeconds float64) error
	MarkFailed(ctx context.Context, id string) error
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

// JobRepository defines relational persistence for processing jobs.
type JobRepository interface {
	CreateJob(ctx context.Context, videoID, workerID string, maxAttempts int) (*ProcessingJob, error)
	MarkJobProcessing(ctx context.Context, jobID, workerID string) error
	MarkJobCompleted(ctx context.Context, jobID string) error
	MarkJobFailed(ctx context.Context, jobID, errorMessage string) error
	MarkJobDLQ(ctx context.Context, jobID, errorMessage string) error
	GetActiveJobByVideoID(ctx context.Context, videoID string) (*ProcessingJob, error)
	GetJobByID(ctx context.Context, jobID string) (*ProcessingJob, error)
}

// RenditionRepository defines relational persistence for generated ABR tracks.
type RenditionRepository interface {
	SaveRenditions(ctx context.Context, videoID string, renditions []Rendition) error
	ListRenditions(ctx context.Context, videoID string) ([]Rendition, error)
}

// Transcoder defines the media transformation engine contract (Docker FFmpeg).
type Transcoder interface {
	TranscodeToHLS(ctx context.Context, sourcePath, outputDir, playlistName string) error
	ProbeMedia(ctx context.Context, localSourcePath string) (*MediaMetadata, error)
	TranscodeABR(ctx context.Context, localSourcePath, outputDir string, cfg ABRConfig) ([]Rendition, error)
	ExtractThumbnail(ctx context.Context, localSourcePath, outputPath string, atSecond float64) error
}
