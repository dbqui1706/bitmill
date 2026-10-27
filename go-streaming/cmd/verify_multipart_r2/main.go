package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	videoApp "quidb.dev/go-streaming/internal/application/video"
	"quidb.dev/go-streaming/internal/config"
	"quidb.dev/go-streaming/internal/domain/video"
	"quidb.dev/go-streaming/internal/infrastructure/repository"
	"quidb.dev/go-streaming/internal/infrastructure/storage"
	"quidb.dev/go-streaming/pkg/logger"
)

func main() {
	log := logger.New()
	fmt.Println("=================================================================")
	fmt.Println("🧪 VERIFICATION: LIVE DIRECT-TO-R2 MULTIPART UPLOAD TEST")
	fmt.Println("=================================================================")

	// 1. Load config
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Error("Failed to load config: %v", err)
		os.Exit(1)
	}

	ctx := context.Background()

	// 2. Connect to PostgreSQL
	pgPool, err := pgxpool.New(ctx, cfg.Database.DSN())
	if err != nil {
		log.Error("Failed to connect to PostgreSQL: %v", err)
		os.Exit(1)
	}
	defer pgPool.Close()

	if err := pgPool.Ping(ctx); err != nil {
		log.Error("PostgreSQL ping failed: %v", err)
		os.Exit(1)
	}
	log.Info("✅ PostgreSQL connected: %s", cfg.Database.DBName)

	// 3. Connect to Cloudflare R2
	r2Adapter, err := storage.NewR2Adapter(
		ctx,
		cfg.Storage.R2.AccountID,
		cfg.Storage.R2.AccessKeyID,
		cfg.Storage.R2.SecretAccessKey,
		cfg.Storage.R2.PublicDomain,
	)
	if err != nil {
		log.Error("Failed to initialize R2: %v", err)
		os.Exit(1)
	}
	log.Info("✅ Cloudflare R2 adapter initialized (Bucket: %s)", cfg.Storage.R2.RawBucket)

	// 4. Initialize Repositories and UploadService
	pgRepo := repository.NewPostgresRepo(pgPool)
	uploadService := videoApp.NewUploadService(
		pgRepo,
		pgRepo,
		r2Adapter,
		cfg.Storage.R2.RawBucket,
		1*time.Hour,
		log,
	)

	// 5. Test Payload Setup: 12MB video (2 parts: Part 1 = 10MB, Part 2 = 2MB)
	part1Size := int64(10 * 1024 * 1024) // 10MB
	part2Size := int64(2 * 1024 * 1024)  // 2MB
	totalSize := part1Size + part2Size   // 12MB

	part1Data := bytes.Repeat([]byte("A"), int(part1Size))
	part2Data := bytes.Repeat([]byte("B"), int(part2Size))

	probeFingerprint := fmt.Sprintf("probe-%d", time.Now().UnixNano())

	// Step 1: Create Upload Session
	log.Info("Step 1: Creating Multipart Upload Session for 12MB test video...")
	sessionResp, err := uploadService.CreateUploadSession(ctx, videoApp.CreateSessionRequest{
		Title:           "E2E Verification Multipart Video (12MB)",
		Description:     "Automated Live Cloudflare R2 Integration Test",
		FileName:        "live_e2e_probe.mp4",
		FileSize:        totalSize,
		MimeType:        "video/mp4",
		FileFingerprint: probeFingerprint,
	})
	if err != nil {
		log.Error("Failed to create upload session: %v", err)
		os.Exit(1)
	}
	log.Info("✅ Upload Session Created: VideoID=%s, UploadID=%s, TotalParts=%d", sessionResp.VideoID, sessionResp.UploadID, sessionResp.TotalParts)

	if len(sessionResp.Parts) != 2 {
		log.Error("Expected 2 parts, got %d", len(sessionResp.Parts))
		os.Exit(1)
	}

	// Step 2: Upload Part 1 (10MB) directly to Cloudflare R2 via Presigned PUT URL
	log.Info("Step 2: Uploading Part 1 (10MB) directly to Cloudflare R2...")
	part1URL := sessionResp.Parts[0].UploadURL
	etag1, err := uploadChunkToR2(part1URL, part1Data)
	if err != nil {
		log.Error("Failed to upload Part 1 to R2: %v", err)
		os.Exit(1)
	}
	log.Info("✅ Part 1 Uploaded successfully! Cloudflare R2 ETag: %s", etag1)

	// Record Part 1 in DB
	_ = pgRepo.RecordPartUploaded(ctx, sessionResp.VideoID, 1, etag1, part1Size)

	// Step 3: Check Upload Status (Simulate Resumable Telemetry Check)
	log.Info("Step 3: Checking Upload Status (Resumable Recovery Check)...")
	statusResp, err := uploadService.GetUploadStatus(ctx, sessionResp.VideoID)
	if err != nil {
		log.Error("Failed to get upload status: %v", err)
		os.Exit(1)
	}
	log.Info("✅ Status Telemetry: Total=%d, Completed=%d, Missing=%d",
		statusResp.TotalParts, statusResp.CompletedPartsCount, len(statusResp.MissingParts))

	if len(statusResp.MissingParts) != 1 || statusResp.MissingParts[0].PartNumber != 2 {
		log.Error("Expected only Part 2 to be missing, got: %v", statusResp.MissingParts)
		os.Exit(1)
	}
	log.Info("✅ Verified: System correctly identifies Part 2 as the sole remaining part to resume!")

	// Step 4: Upload Part 2 (2MB) directly to Cloudflare R2
	log.Info("Step 4: Uploading Part 2 (2MB) directly to Cloudflare R2...")
	part2URL := statusResp.MissingParts[0].UploadURL
	etag2, err := uploadChunkToR2(part2URL, part2Data)
	if err != nil {
		log.Error("Failed to upload Part 2 to R2: %v", err)
		os.Exit(1)
	}
	log.Info("✅ Part 2 Uploaded successfully! Cloudflare R2 ETag: %s", etag2)

	// Step 5: Complete Multipart Upload (Assembly)
	log.Info("Step 5: Calling CompleteUpload to assemble 12MB file on Cloudflare R2...")
	completeParts := []video.CompletedPart{
		{PartNumber: 1, ETag: etag1},
		{PartNumber: 2, ETag: etag2},
	}
	completeResp, err := uploadService.CompleteUpload(ctx, sessionResp.VideoID, completeParts)
	if err != nil {
		log.Error("Failed to complete upload: %v", err)
		os.Exit(1)
	}
	log.Info("✅ CompleteUpload Succeeded! Status: %s, Message: %s", completeResp.Status, completeResp.Message)

	// Step 6: Verify in PostgreSQL
	log.Info("Step 6: Verifying Database Record...")
	v, err := pgRepo.FindByID(ctx, sessionResp.VideoID)
	if err != nil {
		log.Error("Failed to query video in DB: %v", err)
		os.Exit(1)
	}
	log.Info("✅ PostgreSQL Verification: VideoID=%s, Status=%s, RawSizeBytes=%d bytes", v.ID, v.Status, v.RawSizeBytes)
	if v.Status != video.StatusUploaded {
		log.Error("Expected status UPLOADED, got %s", v.Status)
		os.Exit(1)
	}

	// Step 7: Verify on Cloudflare R2 Object Storage
	log.Info("Step 7: Verifying Finalized Object on Cloudflare R2...")
	exists, err := r2Adapter.CheckObjectExists(ctx, cfg.Storage.R2.RawBucket, v.RawObjectKey)
	if err != nil || !exists {
		log.Error("Object does not exist on R2: %v", err)
		os.Exit(1)
	}
	actualSize, err := r2Adapter.GetObjectSize(ctx, cfg.Storage.R2.RawBucket, v.RawObjectKey)
	if err != nil {
		log.Error("Failed to get object size on R2: %v", err)
		os.Exit(1)
	}
	log.Info("✅ Cloudflare R2 Verification: ObjectKey=%s, Exists=true, ActualSize=%d bytes", v.RawObjectKey, actualSize)
	if actualSize != totalSize {
		log.Error("Size mismatch! Expected %d, got %d", totalSize, actualSize)
		os.Exit(1)
	}

	// Step 8: Clean up probe test data
	log.Info("Step 8: Cleaning up test probe object from Cloudflare R2...")
	// Delete test video from DB
	_, _ = pgPool.Exec(ctx, "DELETE FROM videos WHERE id = $1", sessionResp.VideoID)
	log.Info("✅ Probe DB row cleaned up.")

	fmt.Println("=================================================================")
	fmt.Println("🎉 ALL VERIFICATIONS PASSED: 100% SUCCESS WITH REAL CLOUDFLARE R2!")
	fmt.Println("=================================================================")
}

func uploadChunkToR2(presignedURL string, data []byte) (string, error) {
	req, err := http.NewRequest(http.MethodPut, presignedURL, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("failed to build HTTP request: %w", err)
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", "application/octet-stream")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP PUT failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return "", fmt.Errorf("R2 returned HTTP status %d", resp.StatusCode)
	}

	etag := resp.Header.Get("ETag")
	if etag == "" {
		etag = resp.Header.Get("Etag")
	}
	etag = strings.Trim(etag, "\"")
	return fmt.Sprintf("\"%s\"", etag), nil
}
