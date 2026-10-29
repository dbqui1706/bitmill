package transcoder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"quidb.dev/go-streaming/internal/domain/video"
)

var _ video.Transcoder = (*NativeFFmpegTranscoder)(nil)

// NativeFFmpegTranscoder executes media inspection and ABR HLS transcoding using local/embedded FFmpeg binaries directly.
// This eliminates the requirement for /var/run/docker.sock inside production containers.
type NativeFFmpegTranscoder struct {
	ffmpegPath      string
	ffprobePath     string
	gopSize         int
	segmentDuration int
}

// NewNativeFFmpegTranscoder initializes a native binary FFmpeg transcoder
func NewNativeFFmpegTranscoder(ffmpegPath, ffprobePath string, gopSize, segmentDuration int) *NativeFFmpegTranscoder {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}
	if gopSize <= 0 {
		gopSize = 60
	}
	if segmentDuration <= 0 {
		segmentDuration = 2
	}
	return &NativeFFmpegTranscoder{
		ffmpegPath:      ffmpegPath,
		ffprobePath:     ffprobePath,
		gopSize:         gopSize,
		segmentDuration: segmentDuration,
	}
}

// ProbeMedia extracts media duration, codecs, bitrate, and dimensions using native ffprobe
func (t *NativeFFmpegTranscoder) ProbeMedia(ctx context.Context, localSourcePath string) (*video.MediaMetadata, error) {
	if strings.TrimSpace(localSourcePath) == "" {
		return nil, errors.New("cannot probe media: localSourcePath cannot be empty")
	}
	fi, err := os.Stat(localSourcePath)
	if err != nil {
		return nil, fmt.Errorf("media source does not exist: %w", err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("localSourcePath %q is a directory, expected a media file", localSourcePath)
	}

	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		localSourcePath,
	}

	cmd := exec.CommandContext(ctx, t.ffprobePath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("native ffprobe probe failed: %w, log: %s", err, string(out))
	}

	return parseProbeJSON(out)
}

// TranscodeABR transforms source media into Multi-Bitrate ABR HLS using single-pass multi-output FFmpeg
func (t *NativeFFmpegTranscoder) TranscodeABR(ctx context.Context, localSourcePath, outputDir string, cfg video.ABRConfig) ([]video.Rendition, error) {
	if len(cfg.Renditions) == 0 {
		return nil, errors.New("cannot transcode without rendition profiles")
	}
	if cfg.GOPSize <= 0 {
		cfg.GOPSize = t.gopSize
	}
	if cfg.SegmentDuration <= 0 {
		cfg.SegmentDuration = t.segmentDuration
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output dir: %w", err)
	}

	for _, r := range cfg.Renditions {
		if err := os.MkdirAll(filepath.Join(outputDir, r.Name), 0755); err != nil {
			return nil, fmt.Errorf("failed to create rendition dir %s: %w", r.Name, err)
		}
	}

	// Probe audio to check if input has an audio stream
	probe, probeErr := t.ProbeMedia(ctx, localSourcePath)
	hasAudio := probeErr == nil && probe.AudioCodec != ""

	numRends := len(cfg.Renditions)
	var filterComplex string
	if numRends == 1 {
		r := cfg.Renditions[0]
		filterComplex = fmt.Sprintf("[0:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,format=yuv420p[v0out]",
			r.Width, r.Height, r.Width, r.Height)
	} else {
		var splits []string
		for i := 0; i < numRends; i++ {
			splits = append(splits, fmt.Sprintf("[v%din]", i))
		}
		filterComplex = fmt.Sprintf("[0:v]split=%d%s;", numRends, strings.Join(splits, ""))
		for i, r := range cfg.Renditions {
			filterComplex += fmt.Sprintf("[v%din]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,format=yuv420p[v%dout];",
				i, r.Width, r.Height, r.Width, r.Height, i)
		}
		filterComplex = strings.TrimSuffix(filterComplex, ";")
	}

	var mapsAndCodecs []string
	var varStreamMap []string

	for i, r := range cfg.Renditions {
		vBitrate := r.VideoBitrateKbps
		maxrate := int(float64(vBitrate) * 1.1)
		bufsize := vBitrate * 2

		mapsAndCodecs = append(mapsAndCodecs,
			"-map", fmt.Sprintf("[v%dout]", i),
			fmt.Sprintf("-c:v:%d", i), "libx264",
			"-preset", "veryfast",
			fmt.Sprintf("-b:v:%d", i), fmt.Sprintf("%dk", vBitrate),
			fmt.Sprintf("-maxrate:v:%d", i), fmt.Sprintf("%dk", maxrate),
			fmt.Sprintf("-bufsize:v:%d", i), fmt.Sprintf("%dk", bufsize),
			fmt.Sprintf("-g:v:%d", i), strconv.Itoa(cfg.GOPSize),
			fmt.Sprintf("-keyint_min:v:%d", i), strconv.Itoa(cfg.GOPSize),
			fmt.Sprintf("-sc_threshold:v:%d", i), "0",
		)

		if hasAudio {
			mapsAndCodecs = append(mapsAndCodecs,
				"-map", "0:a:0",
				fmt.Sprintf("-c:a:%d", i), "aac",
				fmt.Sprintf("-b:a:%d", i), fmt.Sprintf("%dk", r.AudioBitrateKbps),
				fmt.Sprintf("-ac:a:%d", i), "2",
			)
			varStreamMap = append(varStreamMap, fmt.Sprintf("v:%d,a:%d,name:%s", i, i, r.Name))
		} else {
			varStreamMap = append(varStreamMap, fmt.Sprintf("v:%d,name:%s", i, r.Name))
		}
	}

	hlsArgs := []string{
		"-f", "hls",
		"-hls_time", strconv.Itoa(cfg.SegmentDuration),
		"-hls_playlist_type", "vod",
		"-hls_flags", "independent_segments",
		"-master_pl_name", "master.m3u8",
		"-hls_segment_filename", filepath.ToSlash(filepath.Join(outputDir, "%v", "segment_%03d.ts")),
		"-var_stream_map", strings.Join(varStreamMap, " "),
		filepath.ToSlash(filepath.Join(outputDir, "%v", "playlist.m3u8")),
	}

	args := []string{
		"-nostdin",
		"-y",
		"-protocol_whitelist", "file,crypto,data",
		"-probesize", "10000000",
		"-analyzeduration", "10000000",
		"-i", localSourcePath,
		"-filter_complex", filterComplex,
	}
	args = append(args, mapsAndCodecs...)
	args = append(args, hlsArgs...)

	cmd := exec.CommandContext(ctx, t.ffmpegPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("native ffmpeg ABR transcoding failed: %w, log: %s", err, string(out))
	}

	var generatedRenditions []video.Rendition
	for _, r := range cfg.Renditions {
		rendDir := filepath.Join(outputDir, r.Name)
		files, _ := os.ReadDir(rendDir)
		segCount := 0
		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".ts") {
				segCount++
			}
		}

		generatedRenditions = append(generatedRenditions, video.Rendition{
			ResolutionName:   r.Name,
			Width:            r.Width,
			Height:           r.Height,
			VideoBitrateKbps: r.VideoBitrateKbps,
			AudioBitrateKbps: r.AudioBitrateKbps,
			PlaylistPath:     fmt.Sprintf("%s/playlist.m3u8", r.Name),
			SegmentCount:     segCount,
			Status:           "READY",
		})
	}

	return generatedRenditions, nil
}

// ExtractThumbnail captures a video frame poster at the specified second using native ffmpeg
func (t *NativeFFmpegTranscoder) ExtractThumbnail(ctx context.Context, localSourcePath, outputPath string, atSecond float64) error {
	if strings.TrimSpace(localSourcePath) == "" {
		return errors.New("cannot extract thumbnail: localSourcePath cannot be empty")
	}
	fi, err := os.Stat(localSourcePath)
	if err != nil {
		return fmt.Errorf("media source does not exist: %w", err)
	}
	if fi.IsDir() {
		return fmt.Errorf("localSourcePath %q is a directory, expected a media file", localSourcePath)
	}

	if atSecond < 0 {
		atSecond = 1.0
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("failed to create thumbnail dir: %w", err)
	}

	args := []string{
		"-nostdin",
		"-y",
		"-ss", fmt.Sprintf("%.2f", atSecond),
		"-i", localSourcePath,
		"-frames:v", "1",
		"-update", "1",
		"-q:v", "2",
		outputPath,
	}

	cmd := exec.CommandContext(ctx, t.ffmpegPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("native thumbnail extraction failed: %w, log: %s", err, string(out))
	}

	return nil
}

// TranscodeToHLS implements single-bitrate HLS transcoding using native ffmpeg
func (t *NativeFFmpegTranscoder) TranscodeToHLS(ctx context.Context, sourcePath, outputDir, playlistName string) error {
	if strings.TrimSpace(sourcePath) == "" {
		return errors.New("cannot transcode to HLS: sourcePath cannot be empty")
	}
	fi, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("source file does not exist: %w", err)
	}
	if fi.IsDir() {
		return fmt.Errorf("sourcePath %q is a directory, expected a media file", sourcePath)
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output dir: %w", err)
	}

	playlistTarget := filepath.Join(outputDir, playlistName)
	segmentPattern := filepath.Join(outputDir, "segment_%03d.ts")

	args := []string{
		"-y",
		"-i", sourcePath,
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

	cmd := exec.CommandContext(ctx, t.ffmpegPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("native ffmpeg error: %w, log: %s", err, string(out))
	}

	return nil
}
