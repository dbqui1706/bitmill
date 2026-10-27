package video

import (
	"context"
	"time"
)

// CompletedPart represents an uploaded chunk with its part number and verification ETag.
type CompletedPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}

// StorageProvider defines the domain contract for object storage operations (Cloudflare R2 / S3).
// This interface remains 100% free of external SDK dependencies (Clean Architecture / IoC).
type StorageProvider interface {
	// GeneratePresignedPutURL creates a time-limited signed URL allowing direct single-part HTTP PUT upload
	GeneratePresignedPutURL(ctx context.Context, bucket, objectKey string, expiry time.Duration, contentType string) (string, error)

	// CreateMultipartUpload initiates a multi-part upload session on the storage provider
	CreateMultipartUpload(ctx context.Context, bucket, objectKey, contentType string) (uploadID string, err error)

	// GeneratePresignedPartURL generates a presigned URL for uploading a specific chunk (partNumber)
	GeneratePresignedPartURL(ctx context.Context, bucket, objectKey, uploadID string, partNumber int, expiry time.Duration) (string, error)

	// CompleteMultipartUpload assembles all uploaded parts on storage into a single finalized object
	CompleteMultipartUpload(ctx context.Context, bucket, objectKey, uploadID string, parts []CompletedPart) (string, error)

	// AbortMultipartUpload cancels the multipart upload and purges all staged parts from storage
	AbortMultipartUpload(ctx context.Context, bucket, objectKey, uploadID string) error

	// ListUploadedParts lists all parts that have been successfully uploaded to the storage provider
	ListUploadedParts(ctx context.Context, bucket, objectKey, uploadID string) ([]CompletedPart, error)

	// CheckObjectExists checks whether an object exists in the target storage bucket
	CheckObjectExists(ctx context.Context, bucket, objectKey string) (bool, error)

	// GetObjectSize returns the size of the object in bytes
	GetObjectSize(ctx context.Context, bucket, objectKey string) (int64, error)

	// GetDownloadURL generates a signed or public URL for reading/streaming an object
	GetDownloadURL(ctx context.Context, bucket, objectKey string, expiry time.Duration) (string, error)
}
