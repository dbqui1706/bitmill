package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// GeneratePresignedPutURL generates a temporary presigned PUT URL for direct client uploads
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
		return 0, err
	}
	if resp.ContentLength != nil {
		return *resp.ContentLength, nil
	}
	return 0, nil
}

// GetDownloadURL returns the streaming/download URL (using public domain if set, otherwise Presigned GET)
func (r *R2Adapter) GetDownloadURL(ctx context.Context, bucket, objectKey string, expiry time.Duration) (string, error) {
	if r.publicDomain != "" {
		trimmedDomain := strings.TrimRight(r.publicDomain, "/")
		trimmedKey := strings.TrimLeft(objectKey, "/")
		return fmt.Sprintf("%s/%s", trimmedDomain, trimmedKey), nil
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
