package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsConfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"quidb.dev/go-streaming/internal/domain/video"
)

var _ video.StorageProvider = (*R2Adapter)(nil)

type R2Adapter struct {
	client        *s3.Client
	presignClient *s3.PresignClient
	publicDomain  string
}

// NewR2Adapter initializes an S3 client configured for Cloudflare R2
func NewR2Adapter(ctx context.Context, accountID, accessKeyID, secretAccessKey, publicDomain string) (*R2Adapter, error) {
	if accountID == "" || accessKeyID == "" || secretAccessKey == "" {
		return nil, errors.New("missing Cloudflare R2 credentials (account_id, access_key_id, secret_access_key)")
	}

	r2Endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)

	cfg, err := awsConfig.LoadDefaultConfig(ctx,
		awsConfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
		awsConfig.WithRegion("auto"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS configuration: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(r2Endpoint)
		o.Region = "auto"
	})

	presignClient := s3.NewPresignClient(client)

	return &R2Adapter{
		client:        client,
		presignClient: presignClient,
		publicDomain:  publicDomain,
	}, nil
}

// GeneratePresignedPutURL generates a temporary presigned PUT URL for single-part client uploads
func (r *R2Adapter) GeneratePresignedPutURL(ctx context.Context, bucket, objectKey string, expiry time.Duration, contentType string) (string, error) {
	input := &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}

	req, err := r.presignClient.PresignPutObject(ctx, input, func(opts *s3.PresignOptions) {
		opts.Expires = expiry
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned PUT url: %w", err)
	}

	return req.URL, nil
}

// CreateMultipartUpload initiates a multi-part upload session on Cloudflare R2
func (r *R2Adapter) CreateMultipartUpload(ctx context.Context, bucket, objectKey, contentType string) (string, error) {
	input := &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}

	resp, err := r.client.CreateMultipartUpload(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to create multipart upload on R2: %w", err)
	}

	return *resp.UploadId, nil
}

// GeneratePresignedPartURL generates a presigned URL for uploading a specific chunk/part
func (r *R2Adapter) GeneratePresignedPartURL(ctx context.Context, bucket, objectKey, uploadID string, partNumber int, expiry time.Duration) (string, error) {
	input := &s3.UploadPartInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String(objectKey),
		UploadId:   aws.String(uploadID),
		PartNumber: aws.Int32(int32(partNumber)),
	}

	req, err := r.presignClient.PresignUploadPart(ctx, input, func(opts *s3.PresignOptions) {
		opts.Expires = expiry
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned part url for part %d: %w", partNumber, err)
	}

	return req.URL, nil
}

// CompleteMultipartUpload assembles all uploaded parts on R2 into a single object
func (r *R2Adapter) CompleteMultipartUpload(ctx context.Context, bucket, objectKey, uploadID string, parts []video.CompletedPart) (string, error) {
	var completedParts []types.CompletedPart
	for _, p := range parts {
		completedParts = append(completedParts, types.CompletedPart{
			PartNumber: aws.Int32(int32(p.PartNumber)),
			ETag:       aws.String(p.ETag),
		})
	}

	input := &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(objectKey),
		UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: completedParts,
		},
	}

	resp, err := r.client.CompleteMultipartUpload(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to complete multipart upload on R2: %w", err)
	}

	location := ""
	if resp.Location != nil {
		location = *resp.Location
	}
	return location, nil
}

// AbortMultipartUpload cancels a multipart upload and releases staged storage chunks on R2
func (r *R2Adapter) AbortMultipartUpload(ctx context.Context, bucket, objectKey, uploadID string) error {
	_, err := r.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(objectKey),
		UploadId: aws.String(uploadID),
	})
	if err != nil {
		return fmt.Errorf("failed to abort multipart upload on R2: %w", err)
	}
	return nil
}

// ListUploadedParts lists all chunks that have been successfully uploaded to R2
func (r *R2Adapter) ListUploadedParts(ctx context.Context, bucket, objectKey, uploadID string) ([]video.CompletedPart, error) {
	resp, err := r.client.ListParts(ctx, &s3.ListPartsInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(objectKey),
		UploadId: aws.String(uploadID),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list uploaded parts from R2: %w", err)
	}

	var res []video.CompletedPart
	for _, p := range resp.Parts {
		etag := ""
		if p.ETag != nil {
			etag = *p.ETag
		}
		partNum := 0
		if p.PartNumber != nil {
			partNum = int(*p.PartNumber)
		}
		res = append(res, video.CompletedPart{
			PartNumber: partNum,
			ETag:       etag,
		})
	}
	return res, nil
}

// CheckObjectExists verifies if an object exists in the bucket via HeadObject
func (r *R2Adapter) CheckObjectExists(ctx context.Context, bucket, objectKey string) (bool, error) {
	_, err := r.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		var notFound *types.NotFound
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &notFound) || errors.As(err, &noSuchKey) {
			return false, nil
		}
		// S3 HeadObject returns 404 error responses
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "NotFound") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GetObjectSize returns the byte length of the object
func (r *R2Adapter) GetObjectSize(ctx context.Context, bucket, objectKey string) (int64, error) {
	resp, err := r.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get object size for %s: %w", objectKey, err)
	}

	if resp.ContentLength != nil {
		return *resp.ContentLength, nil
	}
	return 0, nil
}

// GetDownloadURL returns the streaming/read URL for an object
func (r *R2Adapter) GetDownloadURL(ctx context.Context, bucket, objectKey string, expiry time.Duration) (string, error) {
	if r.publicDomain != "" {
		domain := strings.TrimSuffix(r.publicDomain, "/")
		key := strings.TrimPrefix(objectKey, "/")
		return fmt.Sprintf("%s/%s", domain, key), nil
	}

	req, err := r.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = expiry
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned GET url: %w", err)
	}

	return req.URL, nil
}

// DownloadToFile streams an object directly from storage to a local file descriptor
func (r *R2Adapter) DownloadToFile(ctx context.Context, bucket, objectKey, localPath string) error {
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	resp, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return fmt.Errorf("failed to get object from R2: %w", err)
	}
	defer resp.Body.Close()

	outFile, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create local file: %w", err)
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, resp.Body); err != nil {
		return fmt.Errorf("failed to write object to file: %w", err)
	}

	return nil
}

// UploadFile uploads a single local file to Cloudflare R2 with appropriate headers
func (r *R2Adapter) UploadFile(ctx context.Context, localPath, bucket, objectKey, contentType, cacheControl string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file %s: %w", localPath, err)
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(localPath))
	if contentType == "" {
		switch ext {
		case ".m3u8":
			contentType = "application/vnd.apple.mpegurl"
		case ".ts":
			contentType = "video/mp2t"
		case ".jpg", ".jpeg":
			contentType = "image/jpeg"
		case ".png":
			contentType = "image/png"
		case ".mp4":
			contentType = "video/mp4"
		default:
			contentType = "application/octet-stream"
		}
	}

	if cacheControl == "" {
		switch ext {
		case ".m3u8":
			cacheControl = "public, max-age=60"
		case ".ts":
			cacheControl = "public, max-age=31536000, immutable"
		case ".jpg", ".jpeg", ".png":
			cacheControl = "public, max-age=86400"
		default:
			cacheControl = "public, max-age=3600"
		}
	}

	input := &s3.PutObjectInput{
		Bucket:       aws.String(bucket),
		Key:          aws.String(objectKey),
		Body:         file,
		ContentType:  aws.String(contentType),
		CacheControl: aws.String(cacheControl),
	}

	if _, err := r.client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("failed to put object %s to R2: %w", objectKey, err)
	}

	return nil
}

// UploadDirectory recursively uploads all files from a directory to Cloudflare R2 using a worker pool
func (r *R2Adapter) UploadDirectory(ctx context.Context, localDir, bucket, prefix string) error {
	type uploadTask struct {
		localPath string
		remoteKey string
	}

	var tasks []uploadTask
	err := filepath.Walk(localDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(localDir, p)
		if err != nil {
			return err
		}

		// Ensure forward slash in S3 key
		slashRelPath := filepath.ToSlash(relPath)
		var remoteKey string
		if prefix == "" {
			remoteKey = slashRelPath
		} else {
			remoteKey = path.Join(prefix, slashRelPath)
		}

		tasks = append(tasks, uploadTask{
			localPath: p,
			remoteKey: remoteKey,
		})
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to scan directory %s: %w", localDir, err)
	}

	// 16 parallel goroutines to upload segments and manifests quickly
	taskChan := make(chan uploadTask, len(tasks))
	for _, t := range tasks {
		taskChan <- t
	}
	close(taskChan)

	const workerCount = 16
	var wg sync.WaitGroup
	errChan := make(chan error, workerCount)

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskChan {
				select {
				case <-ctx.Done():
					errChan <- ctx.Err()
					return
				default:
					if err := r.UploadFile(ctx, task.localPath, bucket, task.remoteKey, "", ""); err != nil {
						errChan <- fmt.Errorf("failed to upload %s: %w", task.remoteKey, err)
						return
					}
				}
			}
		}()
	}

	wg.Wait()
	close(errChan)

	if firstErr := <-errChan; firstErr != nil {
		return firstErr
	}

	return nil
}
