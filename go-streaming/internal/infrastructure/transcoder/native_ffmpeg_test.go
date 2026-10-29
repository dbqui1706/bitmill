package transcoder

import (
	"context"
	"testing"

	"quidb.dev/go-streaming/internal/domain/video"
)

func TestNativeFFmpegTranscoder_InterfaceCompliance(t *testing.T) {
	var _ video.Transcoder = (*NativeFFmpegTranscoder)(nil)

	trans := NewNativeFFmpegTranscoder("ffmpeg", "ffprobe", 60, 2)
	if trans == nil {
		t.Fatal("expected non-nil NativeFFmpegTranscoder instance")
	}
	if trans.ffmpegPath != "ffmpeg" {
		t.Errorf("expected ffmpegPath to be 'ffmpeg', got %s", trans.ffmpegPath)
	}
	if trans.ffprobePath != "ffprobe" {
		t.Errorf("expected ffprobePath to be 'ffprobe', got %s", trans.ffprobePath)
	}
	if trans.gopSize != 60 {
		t.Errorf("expected gopSize 60, got %d", trans.gopSize)
	}
	if trans.segmentDuration != 2 {
		t.Errorf("expected segmentDuration 2, got %d", trans.segmentDuration)
	}
}

func TestNativeFFmpegTranscoder_EmptySourceValidation(t *testing.T) {
	trans := NewNativeFFmpegTranscoder("", "", 0, 0)
	ctx := context.Background()

	// 1. ProbeMedia with empty path
	_, err := trans.ProbeMedia(ctx, "")
	if err == nil {
		t.Error("expected error probing empty path, got nil")
	}

	// 2. ExtractThumbnail with empty path
	err = trans.ExtractThumbnail(ctx, "", "/tmp/thumb.jpg", 1.0)
	if err == nil {
		t.Error("expected error extracting thumbnail with empty source, got nil")
	}

	// 3. TranscodeToHLS with empty path
	err = trans.TranscodeToHLS(ctx, "", "/tmp/hls", "playlist.m3u8")
	if err == nil {
		t.Error("expected error transcoding HLS with empty source, got nil")
	}

	// 4. TranscodeABR with no renditions
	_, err = trans.TranscodeABR(ctx, "test.mp4", "/tmp/hls", video.ABRConfig{Renditions: nil})
	if err == nil {
		t.Error("expected error transcoding ABR with empty renditions, got nil")
	}
}
