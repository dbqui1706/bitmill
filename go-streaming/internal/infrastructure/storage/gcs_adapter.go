package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"quidb.dev/go-streaming/internal/domain/video"
)

var _ video.StorageProvider = (*GCSStorageAdapter)(nil)

// GCSStorageAdapter implements video.StorageProvider using Google Cloud Storage
type GCSStorageAdapter struct {
	client          *storage.Client
	projectID       string
	clientEmail     string
	privateKeyBytes []byte
	publicDomain    string
}

type serviceAccountFile struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
}

func resolveCredentialsPath(path string) string {
	if path == "" {
		return ""
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if !filepath.IsAbs(path) {
		fallback := filepath.Join("..", path)
		if _, err := os.Stat(fallback); err == nil {
			return fallback
		}
	}
	return path
}

// NewGCSStorageAdapter initializes a Google Cloud Storage client with Service Account credentials
func NewGCSStorageAdapter(ctx context.Context, credentialsFile, projectID, publicDomain string) (*GCSStorageAdapter, error) {
	var opts []option.ClientOption

	var clientEmail string
	var privateKeyBytes []byte

	resolvedCreds := resolveCredentialsPath(credentialsFile)
	if resolvedCreds != "" {
		data, err := os.ReadFile(resolvedCreds)
		if err != nil {
			return nil, fmt.Errorf("failed to read GCP credentials file at %s: %w", credentialsFile, err)
		}

		var sa serviceAccountFile
		if err := json.Unmarshal(data, &sa); err != nil {
			return nil, fmt.Errorf("failed to parse GCP service account credentials: %w", err)
		}

		clientEmail = sa.ClientEmail
		privateKeyBytes = []byte(sa.PrivateKey)
		if projectID == "" {
			projectID = sa.ProjectID
		}

		opts = append(opts, option.WithCredentialsFile(resolvedCreds))
	}

	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Google Cloud Storage client: %w", err)
	}

	return &GCSStorageAdapter{
		client:          client,
		projectID:       projectID,
		clientEmail:     clientEmail,
		privateKeyBytes: privateKeyBytes,
		publicDomain:    strings.TrimSuffix(publicDomain, "/"),
	}, nil
}

// GeneratePresignedPutURL generates a temporary V4 presigned PUT URL for single-part or chunk direct uploads
func (g *GCSStorageAdapter) GeneratePresignedPutURL(ctx context.Context, bucket, objectKey string, expiry time.Duration, contentType string) (string, error) {
	if len(g.privateKeyBytes) == 0 || g.clientEmail == "" {
		return "", errors.New("service account credentials required to generate GCS signed URLs")
	}

	opts := &storage.SignedURLOptions{
		GoogleAccessID: g.clientEmail,
		PrivateKey:     g.privateKeyBytes,
		Method:         http.MethodPut,
		Expires:        time.Now().Add(expiry),
		ContentType:    contentType,
		Scheme:         storage.SigningSchemeV4,
	}

	url, err := storage.SignedURL(bucket, objectKey, opts)
	if err != nil {
		return "", fmt.Errorf("failed to generate GCS V4 signed PUT URL: %w", err)
	}
	return url, nil
}

// CreateMultipartUpload initiates a multi-part upload session placeholder
func (g *GCSStorageAdapter) CreateMultipartUpload(ctx context.Context, bucket, objectKey, contentType string) (string, error) {
	uploadID := fmt.Sprintf("gcs_%d_%s", time.Now().UnixNano(), objectKey)
	return uploadID, nil
}

// GeneratePresignedPartURL generates a presigned URL for uploading a specific part chunk
func (g *GCSStorageAdapter) GeneratePresignedPartURL(ctx context.Context, bucket, objectKey, uploadID string, partNumber int, expiry time.Duration) (string, error) {
	partKey := fmt.Sprintf("tmp_parts/%s/part_%05d", uploadID, partNumber)
	return g.GeneratePresignedPutURL(ctx, bucket, partKey, expiry, "application/octet-stream")
}

// CompleteMultipartUpload combines staged temporary parts into the final target object
func (g *GCSStorageAdapter) CompleteMultipartUpload(ctx context.Context, bucket, objectKey, uploadID string, parts []video.CompletedPart) (string, error) {
	bkt := g.client.Bucket(bucket)
	var srcHandles []*storage.ObjectHandle

	for _, p := range parts {
		partKey := fmt.Sprintf("tmp_parts/%s/part_%05d", uploadID, p.PartNumber)
		srcHandles = append(srcHandles, bkt.Object(partKey))
	}

	dest := bkt.Object(objectKey)
	if len(srcHandles) > 0 {
		composer := dest.ComposerFrom(srcHandles...)
		if _, err := composer.Run(ctx); err != nil {
			return "", fmt.Errorf("failed to compose GCS multipart objects: %w", err)
		}
	}

	// Purge temporary part chunks asynchronously
	go func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		for _, p := range parts {
			partKey := fmt.Sprintf("tmp_parts/%s/part_%05d", uploadID, p.PartNumber)
			_ = bkt.Object(partKey).Delete(cleanupCtx)
		}
	}()

	return fmt.Sprintf("gcs-composed-%s", uploadID), nil
}

// AbortMultipartUpload cleans up temporary staged part chunks
func (g *GCSStorageAdapter) AbortMultipartUpload(ctx context.Context, bucket, objectKey, uploadID string) error {
	bkt := g.client.Bucket(bucket)
	prefix := fmt.Sprintf("tmp_parts/%s/", uploadID)
	it := bkt.Objects(ctx, &storage.Query{Prefix: prefix})
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return err
		}
		_ = bkt.Object(attrs.Name).Delete(ctx)
	}
	return nil
}

// ListUploadedParts lists all staged part chunks for an active session
func (g *GCSStorageAdapter) ListUploadedParts(ctx context.Context, bucket, objectKey, uploadID string) ([]video.CompletedPart, error) {
	bkt := g.client.Bucket(bucket)
	prefix := fmt.Sprintf("tmp_parts/%s/", uploadID)
	it := bkt.Objects(ctx, &storage.Query{Prefix: prefix})

	var parts []video.CompletedPart
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}

		var partNumber int
		base := filepath.Base(attrs.Name)
		_, _ = fmt.Sscanf(base, "part_%d", &partNumber)
		parts = append(parts, video.CompletedPart{
			PartNumber: partNumber,
			ETag:       attrs.Etag,
		})
	}
	return parts, nil
}

// CheckObjectExists checks if an object exists in GCS
func (g *GCSStorageAdapter) CheckObjectExists(ctx context.Context, bucket, objectKey string) (bool, error) {
	_, err := g.client.Bucket(bucket).Object(objectKey).Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// GetObjectSize returns object size in bytes
func (g *GCSStorageAdapter) GetObjectSize(ctx context.Context, bucket, objectKey string) (int64, error) {
	attrs, err := g.client.Bucket(bucket).Object(objectKey).Attrs(ctx)
	if err != nil {
		return 0, err
	}
	return attrs.Size, nil
}

// GetDownloadURL generates a public CDN / storage URL or V4 signed GET URL
func (g *GCSStorageAdapter) GetDownloadURL(ctx context.Context, bucket, objectKey string, expiry time.Duration) (string, error) {
	if g.publicDomain != "" {
		return fmt.Sprintf("%s/%s", g.publicDomain, objectKey), nil
	}

	opts := &storage.SignedURLOptions{
		GoogleAccessID: g.clientEmail,
		PrivateKey:     g.privateKeyBytes,
		Method:         http.MethodGet,
		Expires:        time.Now().Add(expiry),
		Scheme:         storage.SigningSchemeV4,
	}
	return storage.SignedURL(bucket, objectKey, opts)
}

// DownloadToFile streams an object directly to a local file
func (g *GCSStorageAdapter) DownloadToFile(ctx context.Context, bucket, objectKey, localPath string) error {
	rc, err := g.client.Bucket(bucket).Object(objectKey).NewReader(ctx)
	if err != nil {
		return fmt.Errorf("failed to open GCS reader for gs://%s/%s: %w", bucket, objectKey, err)
	}
	defer rc.Close()

	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return err
	}

	outFile, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, rc); err != nil {
		return fmt.Errorf("failed to write GCS stream to %s: %w", localPath, err)
	}
	return nil
}

// UploadFile uploads a single local file to GCS with headers
func (g *GCSStorageAdapter) UploadFile(ctx context.Context, localPath, bucket, objectKey, contentType, cacheControl string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file %s: %w", localPath, err)
	}
	defer file.Close()

	wc := g.client.Bucket(bucket).Object(objectKey).NewWriter(ctx)
	if contentType != "" {
		wc.ContentType = contentType
	}
	if cacheControl != "" {
		wc.CacheControl = cacheControl
	}

	if _, err := io.Copy(wc, file); err != nil {
		_ = wc.Close()
		return fmt.Errorf("failed to write file to GCS gs://%s/%s: %w", bucket, objectKey, err)
	}
	return wc.Close()
}

// UploadDirectory recursively uploads all files in a directory to GCS
func (g *GCSStorageAdapter) UploadDirectory(ctx context.Context, localDir, bucket, prefix string) error {
	var filesToUpload []string
	err := filepath.Walk(localDir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() {
			filesToUpload = append(filesToUpload, p)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to walk local directory %s: %w", localDir, err)
	}

	prefix = strings.Trim(prefix, "/")
	var wg sync.WaitGroup
	errCh := make(chan error, len(filesToUpload))
	sem := make(chan struct{}, 10) // Concurrency limit: 10 concurrent uploads

	for _, localFile := range filesToUpload {
		relPath, err := filepath.Rel(localDir, localFile)
		if err != nil {
			return err
		}

		objectKey := filepath.ToSlash(relPath)
		if prefix != "" {
			objectKey = prefix + "/" + objectKey
		}

		contentType := "application/octet-stream"
		cacheControl := "public, max-age=31536000"
		if strings.HasSuffix(localFile, ".m3u8") {
			contentType = "application/vnd.apple.mpegurl"
			cacheControl = "no-cache, no-store, must-revalidate"
		} else if strings.HasSuffix(localFile, ".ts") {
			contentType = "video/MP2T"
		} else if strings.HasSuffix(localFile, ".mp4") {
			contentType = "video/mp4"
		} else if strings.HasSuffix(localFile, ".jpg") || strings.HasSuffix(localFile, ".jpeg") {
			contentType = "image/jpeg"
		}

		wg.Add(1)
		go func(file, key, ct, cc string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if uploadErr := g.UploadFile(ctx, file, bucket, key, ct, cc); uploadErr != nil {
				errCh <- fmt.Errorf("failed to upload %s: %w", key, uploadErr)
			}
		}(localFile, objectKey, contentType, cacheControl)
	}

	wg.Wait()
	close(errCh)

	for uploadErr := range errCh {
		if uploadErr != nil {
			return uploadErr
		}
	}
	return nil
}

// Close gracefully closes the GCS client
func (g *GCSStorageAdapter) Close() error {
	if g.client != nil {
		return g.client.Close()
	}
	return nil
}
