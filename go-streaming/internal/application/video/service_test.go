package video_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	videoApp "quidb.dev/go-streaming/internal/application/video"
	"quidb.dev/go-streaming/internal/domain/video"
	"quidb.dev/go-streaming/pkg/logger"
)

func TestService_ProcessVideo_R2Object_DispatchesRabbitMQ(t *testing.T) {
	ctx := context.Background()
	log := logger.New()

	vRepo := newMockVideoRepo()
	jRepo := newMockJobRepo()
	qAdapter := &mockJobQueue{}
	transcoder := &mockTranscoder{
		probeFunc: func(ctx context.Context, localSourcePath string) (*video.MediaMetadata, error) {
			t.Fatal("Transcoder should NOT be called for R2 video on API Server")
			return nil, nil
		},
	}

	// Video stored in Cloudflare R2
	testVideo := &video.Video{
		ID:           "test-r2-video",
		UserID:       "user-1",
		Title:        "R2 Video",
		RawBucket:    "bitmill-raw-videos",
		RawObjectKey: "raw/test-r2-video/source.mp4",
		RawSizeBytes: 50 * 1024 * 1024,
		Status:       video.StatusFailed, // Was marked failed by previous bug
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	_ = vRepo.Save(ctx, testVideo)

	svc := videoApp.NewService(vRepo, transcoder, "/tmp/hls", "https://stream.bitmill.dev", log)
	svc.SetTranscodingQueue(jRepo, qAdapter, "bitmill-processed-videos")

	// Call ProcessVideo
	resVideo, err := svc.ProcessVideo(ctx, "test-r2-video")
	assert.NoError(t, err)
	assert.NotNil(t, resVideo)
	assert.Equal(t, video.StatusUploaded, resVideo.Status)

	// Verify message was dispatched to RabbitMQ
	assert.Len(t, qAdapter.published, 1)
	published := qAdapter.published[0]
	assert.Equal(t, "test-r2-video", published.VideoID)
	assert.Equal(t, "raw/test-r2-video/source.mp4", published.SourceKey)
	assert.Equal(t, "bitmill-processed-videos", published.TargetBucket)
}

func TestService_ProcessVideo_EmptySourcePath_ReturnsError(t *testing.T) {
	ctx := context.Background()
	log := logger.New()

	vRepo := newMockVideoRepo()
	transcoder := &mockTranscoder{
		probeFunc: func(ctx context.Context, localSourcePath string) (*video.MediaMetadata, error) {
			t.Fatal("Transcoder should NOT be called when sourcePath is empty")
			return nil, nil
		},
	}

	// Legacy video with no source
	emptyVideo := &video.Video{
		ID:        "empty-video",
		UserID:    "user-1",
		Title:     "Empty Video",
		Status:    video.StatusPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_ = vRepo.Save(ctx, emptyVideo)

	svc := videoApp.NewService(vRepo, transcoder, "/tmp/hls", "https://stream.bitmill.dev", log)

	_, err := svc.ProcessVideo(ctx, "empty-video")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "video has no media source")
}
