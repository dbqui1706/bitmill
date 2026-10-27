package video

import (
	"errors"
	"math"
	"time"
)

type UploadSessionStatus string

const (
	SessionStatusInitializing UploadSessionStatus = "INITIALIZING"
	SessionStatusUploading    UploadSessionStatus = "UPLOADING"
	SessionStatusCompleted    UploadSessionStatus = "COMPLETED"
	SessionStatusAborted      UploadSessionStatus = "ABORTED"
	SessionStatusExpired      UploadSessionStatus = "EXPIRED"
)

type UploadPartStatus string

const (
	PartStatusPending  UploadPartStatus = "PENDING"
	PartStatusUploaded UploadPartStatus = "UPLOADED"
	PartStatusFailed   UploadPartStatus = "FAILED"
)

var (
	ErrInvalidFileSize = errors.New("file size must be between 5MB and 5GB")
	ErrSessionExpired  = errors.New("upload session has expired")
	ErrSessionClosed   = errors.New("upload session is already closed or completed")
)

const (
	MinPartSizeBytes int64 = 5 * 1024 * 1024       // 5MB S3 minimum
	ChunkSize10MB    int64 = 10 * 1024 * 1024      // 10MB for <= 250MB
	ChunkSize20MB    int64 = 20 * 1024 * 1024      // 20MB for > 250MB
	MaxFileSizeBytes int64 = 5 * 1024 * 1024 * 1024 // 5GB
)

// CalculateAdaptiveChunkSize applies RFC-001 economics:
// <= 250MB: 10MB chunk size
// > 250MB: 20MB chunk size
func CalculateAdaptiveChunkSize(fileSizeBytes int64) int64 {
	if fileSizeBytes <= 250*1024*1024 {
		return ChunkSize10MB
	}
	return ChunkSize20MB
}

// CalculateTotalParts calculates total chunk parts needed for a file size.
func CalculateTotalParts(fileSizeBytes, partSizeBytes int64) int {
	if partSizeBytes <= 0 {
		partSizeBytes = ChunkSize20MB
	}
	parts := int(math.Ceil(float64(fileSizeBytes) / float64(partSizeBytes)))
	if parts <= 0 {
		return 1
	}
	return parts
}

// UploadSession represents an active or completed multipart upload session.
type UploadSession struct {
	ID               string              `json:"id"`
	VideoID          string              `json:"video_id"`
	UploadID         string              `json:"upload_id"`
	StorageBucket    string              `json:"storage_bucket"`
	StorageKey       string              `json:"storage_key"`
	FileFingerprint  string              `json:"file_fingerprint"`
	TotalSizeBytes   int64               `json:"total_size_bytes"`
	PartSizeBytes    int64               `json:"part_size_bytes"`
	TotalParts       int                 `json:"total_parts"`
	Status           UploadSessionStatus `json:"status"`
	ExpiresAt        time.Time           `json:"expires_at"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
}

func (s *UploadSession) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

func (s *UploadSession) CanUpload() bool {
	return s.Status == SessionStatusUploading && !s.IsExpired()
}

// UploadPart represents an individual chunk within a multipart upload session.
type UploadPart struct {
	ID              string           `json:"id"`
	UploadSessionID string           `json:"upload_session_id"`
	PartNumber      int              `json:"part_number"`
	ETag            string           `json:"etag"`
	SizeBytes       int64            `json:"size_bytes"`
	Status          UploadPartStatus `json:"status"`
	UploadedAt      *time.Time       `json:"uploaded_at"`
	CreatedAt       time.Time        `json:"created_at"`
}

// PartPresignedURL describes the presigned URL for a given chunk.
type PartPresignedURL struct {
	PartNumber int    `json:"part_number"`
	UploadURL  string `json:"upload_url"`
}
