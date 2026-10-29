package video

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"quidb.dev/go-streaming/internal/domain/video"
	"quidb.dev/go-streaming/pkg/logger"
)

type Service struct {
	repo            video.Repository
	transcoder      video.Transcoder
	jobRepo         video.JobRepository
	jobQueue        video.JobQueue
	processedBucket string
	hlsRootDir      string
	baseURL         string
	log             *logger.Logger
}

func NewService(repo video.Repository, transcoder video.Transcoder, hlsRootDir, baseURL string, log *logger.Logger) *Service {
	return &Service{
		repo:       repo,
		transcoder: transcoder,
		hlsRootDir: hlsRootDir,
		baseURL:    baseURL,
		log:        log,
	}
}

// SetTranscodingQueue wires the RabbitMQ queue and job repository for asynchronous R2 transcoding
func (s *Service) SetTranscodingQueue(jobRepo video.JobRepository, jobQueue video.JobQueue, processedBucket string) {
	s.jobRepo = jobRepo
	s.jobQueue = jobQueue
	s.processedBucket = processedBucket
}

func (s *Service) RegisterVideo(ctx context.Context, id, title, sourcePath string) (*video.Video, error) {
	v, err := video.NewVideo(id, title, sourcePath)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, v); err != nil {
		return nil, fmt.Errorf("lỗi lưu video entity: %w", err)
	}
	s.log.Info("Đã đăng ký video mới: ID=%s, Title='%s'", v.ID, v.Title)
	return v, nil
}

func (s *Service) ProcessVideo(ctx context.Context, id string) (*video.Video, error) {
	v, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// 1. If video is stored on Cloudflare R2 (RFC-001/002)
	if v.RawObjectKey != "" {
		if s.jobQueue == nil || s.jobRepo == nil {
			return nil, fmt.Errorf("transcoding queue is not configured for R2 video %s", id)
		}

		targetBucket := s.processedBucket
		if targetBucket == "" {
			targetBucket = "bitmill-processed-videos"
		}
		targetPrefix := fmt.Sprintf("hls/%s", id)

		jobRecord, err := s.jobRepo.CreateJob(ctx, id, "", 3)
		if err != nil {
			s.log.Error("Failed to record processing job in DB for video %s: %v", id, err)
			return nil, fmt.Errorf("failed to create processing job: %w", err)
		}

		jobMsg := &video.TranscodeJobMessage{
			EventID:      uuid.New().String(),
			EventType:    "video.requeued",
			Timestamp:    time.Now(),
			VideoID:      id,
			JobID:        jobRecord.ID,
			SourceBucket: v.RawBucket,
			SourceKey:    v.RawObjectKey,
			TargetBucket: targetBucket,
			TargetPrefix: targetPrefix,
			AttemptCount: 1,
			MaxAttempts:  3,
		}

		if err := s.jobQueue.PublishTranscodeJob(ctx, jobMsg); err != nil {
			s.log.Error("Failed to publish transcode job to RabbitMQ for video %s: %v", id, err)
			return nil, fmt.Errorf("failed to publish transcode job to RabbitMQ: %w", err)
		}

		// Reset video status to UPLOADED so the worker daemon can consume it
		_ = s.repo.UpdateStatus(ctx, id, video.StatusUploaded)
		v.Status = video.StatusUploaded
		s.log.Info("Successfully queued/re-queued video ID=%s to RabbitMQ (JobID=%s). Waiting for Worker daemon (make worker).", id, jobRecord.ID)
		return v, nil
	}

	// 2. Legacy local file transcoding
	if strings.TrimSpace(v.SourcePath) == "" {
		return nil, errors.New("video has no media source (neither R2 object nor local file)")
	}

	if v.Status == video.StatusProcessing {
		return v, nil
	}

	v.MarkProcessing()
	if err := s.repo.Save(ctx, v); err != nil {
		return nil, err
	}

	outputDir := filepath.Join(s.hlsRootDir, id)
	playlistName := "stream.m3u8"

	// Chạy tác vụ transcode HLS ngầm cho file local
	go func() {
		bgCtx := context.Background()
		s.log.Info("Bắt đầu cắt HLS cho video ID=%s...", id)

		err := s.transcoder.TranscodeToHLS(bgCtx, v.SourcePath, outputDir, playlistName)
		if err != nil {
			s.log.Error("Lỗi khi transcode HLS cho video ID=%s: %v", id, err)
			v.MarkFailed()
			_ = s.repo.Save(bgCtx, v)
			return
		}

		hlsURL := fmt.Sprintf("%s/hls/%s/%s", s.baseURL, id, playlistName)
		v.MarkReady(hlsURL)
		_ = s.repo.Save(bgCtx, v)
		s.log.Info("Video ID=%s đã sẵn sàng stream tại: %s", id, hlsURL)
	}()

	return v, nil
}

func (s *Service) GetVideo(ctx context.Context, id string) (*video.Video, error) {
	return s.repo.FindByID(ctx, id)
}

func (s *Service) ListVideos(ctx context.Context) ([]*video.Video, error) {
	return s.repo.List(ctx)
}
