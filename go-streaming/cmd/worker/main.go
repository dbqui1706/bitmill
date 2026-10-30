package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

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

	fmt.Println("==================================================")
	fmt.Println("🎬 KHỞI ĐỘNG BITMILL TRANSCODING WORKER DAEMON")
	fmt.Println("==================================================")

	// 1. [Configuration]: Load configuration via Viper & .env
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Error("Failed to load configuration: %v", err)
		os.Exit(1)
	}
	log.Info("Worker configuration loaded successfully")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 2. [Database Connection]: PostgreSQL pgxpool
	pgPool, err := pgxpool.New(ctx, cfg.Database.DSN())
	if err != nil {
		log.Error("Failed to connect to PostgreSQL: %v", err)
		os.Exit(1)
	}
	defer pgPool.Close()

	if err := pgPool.Ping(ctx); err != nil {
		log.Error("Failed to ping PostgreSQL: %v", err)
		os.Exit(1)
	}
	log.Info("Connected to PostgreSQL at %s:%d (DB: %s)", cfg.Database.Host, cfg.Database.Port, cfg.Database.DBName)

	// 3. [Storage Provider]: Cloudflare R2 S3 Adapter
	r2Adapter, err := storage.NewR2Adapter(
		ctx,
		cfg.Storage.R2.AccountID,
		cfg.Storage.R2.AccessKeyID,
		cfg.Storage.R2.SecretAccessKey,
		cfg.Storage.R2.PublicDomain,
	)
	if err != nil {
		log.Error("Failed to initialize Cloudflare R2 Adapter: %v", err)
		os.Exit(1)
	}
	log.Info("Cloudflare R2 Adapter connected (Raw: %s, Processed: %s)",
		cfg.Storage.R2.RawBucket, cfg.Storage.R2.ProcessedBucket)

	// 4. [FFmpeg Transcoder: Native vs Docker via IoC]
	var ffmpegTranscoder video.Transcoder
	if cfg.Transcoder.Type == "native" {
		ffmpegTranscoder = transcoder.NewNativeFFmpegTranscoder(
			cfg.Transcoder.FFmpegPath,
			cfg.Transcoder.FFprobePath,
			cfg.Transcoder.GOPSize,
			cfg.Transcoder.HLSSegmentDuration,
		)
		log.Info("Native FFmpeg Media Engine initialized (FFmpeg: %s, FFprobe: %s, GOP: %d, Segment: %ds)",
			cfg.Transcoder.FFmpegPath, cfg.Transcoder.FFprobePath, cfg.Transcoder.GOPSize, cfg.Transcoder.HLSSegmentDuration)
	} else {
		rootDir, err := filepath.Abs(cfg.Storage.RootDir)
		if err != nil {
			rootDir = "."
		}
		ffmpegTranscoder = transcoder.NewDockerFFmpegTranscoder(
			rootDir,
			cfg.Transcoder.DockerImage,
			cfg.Transcoder.GOPSize,
			cfg.Transcoder.HLSSegmentDuration,
		)
		log.Info("Docker FFmpeg Media Engine initialized (Image: %s, GOP: %d, Segment: %ds)",
			cfg.Transcoder.DockerImage, cfg.Transcoder.GOPSize, cfg.Transcoder.HLSSegmentDuration)
	}

	// 4.1 [Worker Health Heartbeat for Container Healthchecks]
	heartbeatFile := os.Getenv("WORKER_HEARTBEAT_FILE")
	if heartbeatFile == "" {
		heartbeatFile = "/tmp/worker.heartbeat"
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = os.Remove(heartbeatFile)
				return
			case <-ticker.C:
				_ = os.WriteFile(heartbeatFile, []byte(fmt.Sprintf("%d\n", time.Now().Unix())), 0644)
			}
		}
	}()

	// 5. [RabbitMQ Message Broker]
	queueCfg := queue.RabbitMQConfig{
		URI:           cfg.RabbitMQ.URI,
		Exchange:      cfg.RabbitMQ.Exchange,
		Queue:         cfg.RabbitMQ.Queue,
		RoutingKey:    cfg.RabbitMQ.RoutingKey,
		DLX:           cfg.RabbitMQ.DLX,
		DLQ:           cfg.RabbitMQ.DLQ,
		PrefetchCount: 1, // Strict prefetch=1 per transcode slot
	}
	rabbitMQAdapter, err := queue.NewRabbitMQAdapter(queueCfg, log)
	if err != nil {
		log.Error("Failed to connect to RabbitMQ broker: %v", err)
		os.Exit(1)
	}
	defer rabbitMQAdapter.Close()
	log.Info("RabbitMQ Consumer connected (Queue: %s, Prefetch: %d)", cfg.RabbitMQ.Queue, queueCfg.PrefetchCount)

	// 6. [Repositories & Worker Application Service]
	pgRepo := repository.NewPostgresRepo(pgPool)
	scratchDir := "/tmp/bitmill/transcode"
	_ = os.MkdirAll(scratchDir, 0755)

	workerService := videoApp.NewWorkerService(
		pgRepo,
		pgRepo,
		pgRepo,
		r2Adapter,
		ffmpegTranscoder,
		rabbitMQAdapter,
		scratchDir,
		cfg.Storage.R2.PublicDomain,
		"", // Auto hostname
		log,
	)

	// 7. [Graceful Shutdown Signal Handling]
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Warn("Received termination signal (%v), initiating graceful worker drain...", sig)
		cancel()
	}()

	// 8. [Start Consumer Loop]
	log.Info("Worker daemon is ready. Awaiting transcode jobs from RabbitMQ...")
	if err := workerService.StartConsumer(ctx); err != nil && err != context.Canceled {
		log.Error("Worker consumer terminated with error: %v", err)
		os.Exit(1)
	}

	log.Info("Worker daemon shutdown completed cleanly")
}
