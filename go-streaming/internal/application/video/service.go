package video

import (
	"context"
	"fmt"
	"path/filepath"

	"quidb.dev/go-streaming/internal/domain/video"
	"quidb.dev/go-streaming/pkg/logger"
)

type Service struct {
	repo        video.Repository
	transcoder  video.Transcoder
	hlsRootDir  string
	baseURL     string
	log         *logger.Logger
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

	if v.Status == video.StatusProcessing {
		return v, nil
	}

	v.MarkProcessing()
	if err := s.repo.Save(ctx, v); err != nil {
		return nil, err
	}

	outputDir := filepath.Join(s.hlsRootDir, id)
	playlistName := "stream.m3u8"

	// Chạy tác vụ transcode HLS ngầm
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
