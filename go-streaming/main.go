package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	videoApp "quidb.dev/go-streaming/internal/application/video"
	"quidb.dev/go-streaming/internal/config"
	"quidb.dev/go-streaming/internal/infrastructure/repository"
	"quidb.dev/go-streaming/internal/infrastructure/transcoder"
	deliveryHTTP "quidb.dev/go-streaming/internal/interfaces/http"
	deliveryRTMP "quidb.dev/go-streaming/internal/interfaces/rtmp"
	"quidb.dev/go-streaming/pkg/logger"
)

func main() {
	log := logger.New()

	fmt.Println("==================================================")
	fmt.Println("🚀 KHỞI ĐỘNG BITMILL STREAMING MEDIA SERVER")
	fmt.Println("==================================================")

	// 1. [Config]: Nạp cấu hình thông qua VIPER
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Error("Không thể load cấu hình qua Viper: %v", err)
		os.Exit(1)
	}
	log.Info("Đã nạp cấu hình thành công bằng VIPER (HTTP Port: %d, RTMP Port: %d)", cfg.Server.HTTPPort, cfg.Server.RTMPPort)

	rootDir, err := filepath.Abs(cfg.Storage.RootDir)
	if err != nil {
		rootDir = "."
	}
	hlsDir, err := filepath.Abs(cfg.Storage.HLSDir)
	if err != nil {
		hlsDir = filepath.Join(rootDir, "hls_storage")
	}
	_ = os.MkdirAll(hlsDir, 0755)

	// 2. [Infrastructure Layer]
	repo := repository.NewMemoryRepo()
	ffmpegTranscoder := transcoder.NewDockerFFmpegTranscoder(
		rootDir,
		cfg.Transcoder.DockerImage,
		cfg.Transcoder.GOPSize,
		cfg.Transcoder.HLSSegmentDuration,
	)

	// 3. [Application Layer]
	service := videoApp.NewService(repo, ffmpegTranscoder, hlsDir, cfg.Server.BaseURL, log)

	// Tự động quét nạp video có sẵn trong repo (video.mp4, sample.mp4)
	registerAvailableVideos(service, rootDir, log)

	// 4. [Interfaces Layer - RTMP Ingest Cổng 1935]
	rtmpAddr := fmt.Sprintf(":%d", cfg.Server.RTMPPort)
	rtmpServer := deliveryRTMP.NewServer(rtmpAddr, log)
	go func() {
		if err := rtmpServer.Start(); err != nil {
			log.Error("Lỗi khởi động RTMP Server: %v", err)
		}
	}()

	// 5. [Interfaces Layer - HTTP GIN Engine Cổng 8080]
	gin.SetMode(gin.DebugMode)
	router := gin.Default()

	httpHandler := deliveryHTTP.NewHandler(service, hlsDir, log)
	httpHandler.RegisterRoutes(router)

	httpAddr := fmt.Sprintf(":%d", cfg.Server.HTTPPort)
	log.Info("HTTP Gin Server đang lắng nghe tại: %s%s", cfg.Server.BaseURL, httpAddr)
	log.Info("👉 Mở trình duyệt truy cập: %s để quản lý và xem video!", cfg.Server.BaseURL)
	fmt.Println("--------------------------------------------------")

	if err := router.Run(httpAddr); err != nil {
		log.Error("Lỗi chạy Gin Server: %v", err)
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
			log.Info("Tự động nạp video có sẵn: '%s' (Path: %s)", c.title, fullPath)
		}
	}
}
