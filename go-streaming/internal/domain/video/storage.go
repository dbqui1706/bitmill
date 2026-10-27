package video

import (
	"context"
	"time"
)

// StorageProvider defines the domain contract for object storage operations (Cloudflare R2 / S3).
// This interface remains 100% free of external SDK dependencies (Clean Architecture).
type StorageProvider interface {
	// GeneratePresignedPutURL creates a time-limited signed URL allowing direct HTTP PUT upload
	GeneratePresignedPutURL(ctx context.Context, bucket, objectKey string, expiry time.Duration, contentType string) (string, error)

	// CheckObjectExists checks whether an object exists in the target storage bucket
	CheckObjectExists(ctx context.Context, bucket, objectKey string) (bool, error)

	// GetObjectSize returns the size of the object in bytes
	GetObjectSize(ctx context.Context, bucket, objectKey string) (int64, error)

	// GetDownloadURL generates a signed or public URL for reading/streaming an object
	GetDownloadURL(ctx context.Context, bucket, objectKey string, expiry time.Duration) (string, error)
}
