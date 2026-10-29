package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	videoApp "quidb.dev/go-streaming/internal/application/video"
	"quidb.dev/go-streaming/internal/config"
	"quidb.dev/go-streaming/internal/infrastructure/queue"
	"quidb.dev/go-streaming/internal/infrastructure/repository"
	"quidb.dev/go-streaming/internal/infrastructure/storage"
	"quidb.dev/go-streaming/internal/infrastructure/transcoder"
	deliveryHTTP "quidb.dev/go-streaming/internal/interfaces/http"
	deliveryRTMP "quidb.dev/go-streaming/internal/interfaces/rtmp"
	"quidb.dev/go-streaming/pkg/logger"
	"quidb.dev/go-streaming/pkg/metrics"
)

func main() {
	log := logger.New()

	fmt.Println("==================================================")
	fmt.Println("🚀 KHỞI ĐỘNG BITMILL STREAMING MEDIA SERVER")
	fmt.Println("==================================================")

	// 1. [Config]: Load configuration via Viper
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Error("Failed to load configuration: %v", err)
		os.Exit(1)
	}
	log.Info("Configuration loaded successfully (HTTP Port: %d, RTMP Port: %d)", cfg.Server.HTTPPort, cfg.Server.RTMPPort)

	// Normalize storage directories
	rootDir, err := filepath.Abs(cfg.Storage.RootDir)
	if err != nil {
		rootDir = "."
	}
	hlsDir, err := filepath.Abs(cfg.Storage.HLSDir)
	if err != nil {
		hlsDir = filepath.Join(rootDir, "hls_storage")
	}
	_ = os.MkdirAll(hlsDir, 0755)

	// 2. [Database Connection Pool (PostgreSQL)]
	ctx := context.Background()
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
	log.Info("Connected to PostgreSQL successfully at %s:%d (DB: %s)", cfg.Database.Host, cfg.Database.Port, cfg.Database.DBName)

	// 3. [Storage Provider (Cloudflare R2 Adapter)]
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
	log.Info("Cloudflare R2 Adapter initialized successfully (Bucket: %s)", cfg.Storage.R2.RawBucket)

	// 4. [Repositories]
	pgRepo := repository.NewPostgresRepo(pgPool)
	ffmpegTranscoder := transcoder.NewDockerFFmpegTranscoder(
		rootDir,
		cfg.Transcoder.DockerImage,
		cfg.Transcoder.GOPSize,
		cfg.Transcoder.HLSSegmentDuration,
	)

	// 5. [Application Services]
	legacyService := videoApp.NewService(pgRepo, ffmpegTranscoder, hlsDir, cfg.Server.BaseURL, log)
	uploadService := videoApp.NewUploadService(pgRepo, pgRepo, r2Adapter, cfg.Storage.R2.RawBucket, 2*time.Hour, log)

	// 5.1 [RabbitMQ Event Dispatcher - Milestone 4]
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
		log.Warn("RabbitMQ connection failed: %v. Transcode events will not be dispatched.", err)
	} else {
		defer rabbitMQAdapter.Close()
		uploadService.SetTranscodingQueue(pgRepo, rabbitMQAdapter, cfg.Storage.R2.ProcessedBucket)
		legacyService.SetTranscodingQueue(pgRepo, rabbitMQAdapter, cfg.Storage.R2.ProcessedBucket)
		log.Info("RabbitMQ Event Dispatcher wired to UploadService and Service (Exchange: %s)", cfg.RabbitMQ.Exchange)
	}

	// Scan available test videos
	registerAvailableVideos(legacyService, rootDir, log)

	// 6. [Interfaces Layer - RTMP Ingest Port 1935]
	rtmpAddr := fmt.Sprintf(":%d", cfg.Server.RTMPPort)
	rtmpServer := deliveryRTMP.NewServer(rtmpAddr, log)
	go func() {
		if err := rtmpServer.Start(); err != nil {
			log.Error("RTMP Server error: %v", err)
		}
	}()

	// 7. [Interfaces Layer - HTTP Gin Engine Port 8080]
	gin.SetMode(gin.DebugMode)
	router := gin.Default()

	// 7.1 [Observability Middleware & Prometheus /metrics Endpoint]
	router.Use(metrics.PrometheusMiddleware())
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Register Handlers
	legacyHandler := deliveryHTTP.NewHandler(legacyService, hlsDir, log)
	legacyHandler.RegisterRoutes(router)

	uploadHandler := deliveryHTTP.NewUploadHandler(uploadService, log)
	uploadHandler.RegisterRoutes(router)

	httpAddr := fmt.Sprintf(":%d", cfg.Server.HTTPPort)
	log.Info("HTTP Gin Server listening on: %s%s", cfg.Server.BaseURL, httpAddr)
	log.Info("👉 Web player dashboard: %s", cfg.Server.BaseURL)
	log.Info("👉 Upload API v1: %s/api/v1/videos/upload-session", cfg.Server.BaseURL)
	fmt.Println("--------------------------------------------------")

	if err := router.Run(httpAddr); err != nil {
		log.Error("Gin Server error: %v", err)
	}
}

func registerAvailableVideos(svc *videoApp.Service, rootDir string, log *logger.Logger) {
	candidates := []struct {
		id       string
		title    string
		filename string
	}{
		{"video-1", "Video Upload Thực Tế (180MB)", "video.mp4"},
		{"sample-1", "Video Test 10s (Synthetic 720p)", "sample.mp4"},
	}

	for _, c := range candidates {
		fullPath := filepath.Join(rootDir, c.filename)
		if _, err := os.Stat(fullPath); err == nil {
			_, _ = svc.RegisterVideo(context.Background(), c.id, c.title, fullPath)
			log.Info("Auto-registered existing video: '%s' (Path: %s)", c.title, fullPath)
		}
	}
}
