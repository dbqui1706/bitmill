package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	videoApp "quidb.dev/go-streaming/internal/application/video"
	"quidb.dev/go-streaming/internal/config"
	"quidb.dev/go-streaming/internal/domain/video"
	"quidb.dev/go-streaming/internal/infrastructure/queue"
	"quidb.dev/go-streaming/internal/infrastructure/repository"
	"quidb.dev/go-streaming/internal/infrastructure/storage"
	"quidb.dev/go-streaming/internal/infrastructure/transcoder"
	"quidb.dev/go-streaming/pkg/logger"
)

func main() {
	log := logger.New()
	fmt.Println("=================================================================")
	fmt.Println("🧪 VERIFICATION: LIVE E2E RABBITMQ & DOCKER TRANSCODING PIPELINE")
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
	log.Info("✅ Step 1: PostgreSQL connected (%s)", cfg.Database.DBName)

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
	log.Info("✅ Step 2: Cloudflare R2 Adapter connected (Raw: %s, Processed: %s)",
		cfg.Storage.R2.RawBucket, cfg.Storage.R2.ProcessedBucket)

	// 4. Connect to RabbitMQ and declare topology
	queueCfg := queue.RabbitMQConfig{
		URI:           cfg.RabbitMQ.URI,
		Exchange:      cfg.RabbitMQ.Exchange,
		Queue:         cfg.RabbitMQ.Queue,
		RoutingKey:    cfg.RabbitMQ.RoutingKey,
		DLX:           cfg.RabbitMQ.DLX,
		DLQ:           cfg.RabbitMQ.DLQ,
		PrefetchCount: 1,
	}
	rabbitMQAdapter, err := queue.NewRabbitMQAdapter(queueCfg, log)
	if err != nil {
		log.Error("Failed to connect to RabbitMQ: %v", err)
		os.Exit(1)
	}
	defer rabbitMQAdapter.Close()
	log.Info("✅ Step 3: RabbitMQ AMQP Topology initialized (Exchange: %s, Queue: %s, DLQ: %s)",
		cfg.RabbitMQ.Exchange, cfg.RabbitMQ.Queue, cfg.RabbitMQ.DLQ)

	// 5. Initialize Docker Transcoder
	rootDir, err := filepath.Abs(cfg.Storage.RootDir)
	if err != nil {
		rootDir = "."
	}
	ffmpegTranscoder := transcoder.NewDockerFFmpegTranscoder(
		rootDir,
		cfg.Transcoder.DockerImage,
		cfg.Transcoder.GOPSize,
		cfg.Transcoder.HLSSegmentDuration,
	)
	log.Info("✅ Step 4: Docker FFmpeg Transcoder initialized (Image: %s)", cfg.Transcoder.DockerImage)

	// 6. Stage sample.mp4 to Cloudflare R2 raw bucket
	samplePath := filepath.Join(rootDir, "sample.mp4")
	if _, err := os.Stat(samplePath); err != nil {
		samplePath = "../sample.mp4"
	}
	sampleInfo, err := os.Stat(samplePath)
	if err != nil {
		log.Error("Could not locate sample.mp4: %v", err)
		os.Exit(1)
	}

	testVideoID := uuid.New().String()
	rawKey := fmt.Sprintf("raw/%s/sample.mp4", testVideoID)
	log.Info("Staging %s (%d bytes) to Cloudflare R2 raw bucket at %s...", samplePath, sampleInfo.Size(), rawKey)
	if err := r2Adapter.UploadFile(ctx, samplePath, cfg.Storage.R2.RawBucket, rawKey, "video/mp4", ""); err != nil {
		log.Error("Failed to upload sample.mp4 to R2: %v", err)
		os.Exit(1)
	}
	log.Info("✅ Step 5: Sample media staged to Cloudflare R2")

	// 7. Register Video in Database as UPLOADED
	pgRepo := repository.NewPostgresRepo(pgPool)
	videoEntity, _ := video.NewR2Video(
		testVideoID,
		"Live E2E Verification Video",
		cfg.Storage.R2.RawBucket,
		rawKey,
		"video/mp4",
		sampleInfo.Size(),
	)
	videoEntity.UserID = uuid.New().String()
	videoEntity.MarkUploaded(sampleInfo.Size())
	if err := pgRepo.Save(ctx, videoEntity); err != nil {
		log.Error("Failed to save video entity: %v", err)
		os.Exit(1)
	}

	jobRecord, err := pgRepo.CreateJob(ctx, testVideoID, "verifier-worker", 3)
	if err != nil {
		log.Error("Failed to create job record: %v", err)
		os.Exit(1)
	}
	log.Info("✅ Step 6: Video (%s) and Job (%s) persisted to PostgreSQL", testVideoID, jobRecord.ID)

	// 8. Publish Transcode Job to RabbitMQ
	jobMsg := &video.TranscodeJobMessage{
		EventID:      uuid.New().String(),
		EventType:    "video.uploaded",
		Timestamp:    time.Now(),
		VideoID:      testVideoID,
		JobID:        jobRecord.ID,
		SourceBucket: cfg.Storage.R2.RawBucket,
		SourceKey:    rawKey,
		TargetBucket: cfg.Storage.R2.ProcessedBucket,
		TargetPrefix: fmt.Sprintf("hls/%s", testVideoID),
		AttemptCount: 1,
		MaxAttempts:  3,
	}

	if err := rabbitMQAdapter.PublishTranscodeJob(ctx, jobMsg); err != nil {
		log.Error("Failed to publish job to RabbitMQ: %v", err)
		os.Exit(1)
	}
	log.Info("✅ Step 7: Transcode job published to RabbitMQ exchange '%s' with confirm", cfg.RabbitMQ.Exchange)

	// 9. Execute Worker Service on the Job
	workerService := videoApp.NewWorkerService(
		pgRepo,
		pgRepo,
		pgRepo,
		r2Adapter,
		ffmpegTranscoder,
		rabbitMQAdapter,
		"/tmp/bitmill/verify_transcode",
		cfg.Storage.R2.PublicDomain,
		"verify-worker",
		log,
	)

	log.Info("Executing worker pipeline on job...")
	startTime := time.Now()
	if err := workerService.ProcessJob(ctx, jobMsg); err != nil {
		log.Error("Worker pipeline failed: %v", err)
		os.Exit(1)
	}
	elapsed := time.Since(startTime)
	log.Info("✅ Step 8: Transcoding pipeline completed in %v!", elapsed)

	// 10. Assertions & Verification
	finalVideo, err := pgRepo.FindByID(ctx, testVideoID)
	if err != nil {
		log.Error("Failed to retrieve final video: %v", err)
		os.Exit(1)
	}

	if finalVideo.Status != video.StatusReady {
		log.Error("❌ Verification failed: Expected status READY, got %s", finalVideo.Status)
		os.Exit(1)
	}

	renditions, err := pgRepo.ListRenditions(ctx, testVideoID)
	if err != nil || len(renditions) == 0 {
		log.Error("❌ Verification failed: No renditions found: %v", err)
		os.Exit(1)
	}

	finalJob, err := pgRepo.GetJobByID(ctx, jobRecord.ID)
	if err != nil || finalJob.Status != video.JobStatusCompleted {
		log.Error("❌ Verification failed: Job status is not COMPLETED: %v", finalJob)
		os.Exit(1)
	}

	// Verify master playlist exists in R2
	masterKey := fmt.Sprintf("hls/%s/master.m3u8", testVideoID)
	masterExists, err := r2Adapter.CheckObjectExists(ctx, cfg.Storage.R2.ProcessedBucket, masterKey)
	if err != nil || !masterExists {
		log.Error("❌ Verification failed: master.m3u8 missing in R2: %v", err)
		os.Exit(1)
	}

	thumbKey := fmt.Sprintf("hls/%s/thumbnail.jpg", testVideoID)
	thumbExists, err := r2Adapter.CheckObjectExists(ctx, cfg.Storage.R2.ProcessedBucket, thumbKey)
	if err != nil || !thumbExists {
		log.Error("❌ Verification failed: thumbnail.jpg missing in R2: %v", err)
		os.Exit(1)
	}

	fmt.Println("=================================================================")
	fmt.Println("🎉 ALL VERIFICATION CHECKS PASSED SUCCESSFULLY!")
	fmt.Println("=================================================================")
	fmt.Printf("• Video ID:               %s\n", finalVideo.ID)
	fmt.Printf("• Video Status:           %s\n", finalVideo.Status)
	fmt.Printf("• Duration:               %.2fs\n", finalVideo.DurationSeconds)
	fmt.Printf("• Master Playlist URL:    %s\n", finalVideo.MasterPlaylistURL)
	fmt.Printf("• Thumbnail URL:          %s\n", finalVideo.ThumbnailURL)
	fmt.Printf("• Generated Renditions:   %d variants:\n", len(renditions))
	for _, r := range renditions {
		fmt.Printf("    - %-6s (%4dx%-4d, %5d kbps, %2d segments) -> %s\n",
			r.ResolutionName, r.Width, r.Height, r.VideoBitrateKbps, r.SegmentCount, r.PlaylistPath)
	}
	fmt.Printf("• Job ID:                 %s (Status: %s)\n", finalJob.ID, finalJob.Status)
	fmt.Printf("• Transcode Duration:     %v\n", elapsed)
	fmt.Println("=================================================================")
}
