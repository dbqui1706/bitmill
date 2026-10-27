package transcoder

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// DockerFFmpegTranscoder thực thi việc cắt HLS bằng FFmpeg container
type DockerFFmpegTranscoder struct {
	rootDir         string
	dockerImage     string
	gopSize         int
	segmentDuration int
}

func NewDockerFFmpegTranscoder(rootDir, dockerImage string, gopSize, segmentDuration int) *DockerFFmpegTranscoder {
	if dockerImage == "" {
		dockerImage = "mwader/static-ffmpeg:latest"
	}
	if gopSize <= 0 {
		gopSize = 60
	}
	if segmentDuration <= 0 {
		segmentDuration = 2
	}
	return &DockerFFmpegTranscoder{
		rootDir:         rootDir,
		dockerImage:     dockerImage,
		gopSize:         gopSize,
		segmentDuration: segmentDuration,
	}
}

func (t *DockerFFmpegTranscoder) TranscodeToHLS(ctx context.Context, sourcePath, outputDir, playlistName string) error {
	// 1. Tạo thư mục chứa các file HLS nếu chưa có
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("lỗi tạo thư mục output: %w", err)
	}

	// 2. Chuẩn hóa đường dẫn tương đối để khớp với mount point /work trong container
	relSource, err := filepath.Rel(t.rootDir, sourcePath)
	if err != nil {
		relSource = sourcePath
	}
	relOutput, err := filepath.Rel(t.rootDir, outputDir)
	if err != nil {
		relOutput = outputDir
	}

	playlistTarget := filepath.Join(relOutput, playlistName)
	segmentPattern := filepath.Join(relOutput, "segment_%03d.ts")

	// 3. Gọi FFmpeg cắt video với GOP và segment duration cấu hình
	args := []string{
		"run", "--rm",
		"-v", fmt.Sprintf("%s:/work", t.rootDir),
		"-w", "/work",
		t.dockerImage,
		"-y",
		"-i", relSource,
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-g", strconv.Itoa(t.gopSize),
		"-keyint_min", strconv.Itoa(t.gopSize),
		"-sc_threshold", "0",
		"-c:a", "aac",
		"-hls_time", strconv.Itoa(t.segmentDuration),
		"-hls_list_size", "0",
		"-hls_segment_filename", segmentPattern,
		playlistTarget,
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg lỗi: %w, log: %s", err, string(out))
	}

	return nil
}
