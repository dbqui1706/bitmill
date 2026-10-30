package transcoder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"quidb.dev/go-streaming/internal/domain/video"
)

var _ video.Transcoder = (*DockerFFmpegTranscoder)(nil)

// DockerFFmpegTranscoder executes media inspection and ABR HLS transcoding via an isolated Docker container
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

type ffprobeStream struct {
	CodecType string `json:"codec_type"`
	CodecName string `json:"codec_name"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

type ffprobeFormat struct {
	Duration string `json:"duration"`
	BitRate  string `json:"bit_rate"`
}

type ffprobeJSON struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

// ProbeMedia extracts media duration, codecs, bitrate, and dimensions using ffprobe inside Docker
func (t *DockerFFmpegTranscoder) ProbeMedia(ctx context.Context, localSourcePath string) (*video.MediaMetadata, error) {
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

	mountDir, relPaths, err := resolveMountAndPaths(localSourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve mount dir for source: %w", err)
	}
	relSource := relPaths[0]

	args := []string{
		"run", "--rm",
		"--network", "none",
		"--entrypoint", "/ffprobe",
		"-v", fmt.Sprintf("%s:/work:ro", mountDir),
		"-w", "/work",
		t.dockerImage,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		relSource,
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ffprobe probe failed: %w, log: %s", err, string(out))
	}

	return parseProbeJSON(out)
}

func parseProbeJSON(out []byte) (*video.MediaMetadata, error) {
	var probe ffprobeJSON
	if err := json.Unmarshal(out, &probe); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ffprobe json output: %w", err)
	}

	var meta video.MediaMetadata
	var foundVideo bool

	for _, s := range probe.Streams {
		if s.CodecType == "video" && !foundVideo {
			foundVideo = true
			meta.Width = s.Width
			meta.Height = s.Height
			meta.VideoCodec = s.CodecName
		} else if s.CodecType == "audio" && meta.AudioCodec == "" {
			meta.AudioCodec = s.CodecName
		}
	}

	if !foundVideo {
		return nil, errors.New("no video stream found in file: corrupt or invalid format")
	}

	if probe.Format.Duration != "" {
		if dur, err := strconv.ParseFloat(probe.Format.Duration, 64); err == nil {
			meta.DurationSeconds = dur
		}
	}
	if probe.Format.BitRate != "" {
		if br, err := strconv.Atoi(probe.Format.BitRate); err == nil {
			meta.BitrateKbps = br / 1000
		}
	}

	return &meta, nil
}

// TranscodeABR transforms source media into Multi-Bitrate ABR HLS using single-pass multi-output FFmpeg
func (t *DockerFFmpegTranscoder) TranscodeABR(ctx context.Context, localSourcePath, outputDir string, cfg video.ABRConfig) ([]video.Rendition, error) {
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

	// Create subdirectories for each rendition
	for _, r := range cfg.Renditions {
		if err := os.MkdirAll(filepath.Join(outputDir, r.Name), 0755); err != nil {
			return nil, fmt.Errorf("failed to create rendition dir %s: %w", r.Name, err)
		}
	}

	// Probe audio to check if input has audio stream
	probe, probeErr := t.ProbeMedia(ctx, localSourcePath)
	hasAudio := probeErr == nil && probe.AudioCodec != ""

	mountDir, relPaths, err := resolveMountAndPaths(localSourcePath, outputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve mount paths: %w", err)
	}
	relSource := relPaths[0]
	relOutput := relPaths[1]

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
		"-hls_segment_filename", filepath.ToSlash(filepath.Join(relOutput, "%v", "segment_%03d.ts")),
		"-var_stream_map", strings.Join(varStreamMap, " "),
		filepath.ToSlash(filepath.Join(relOutput, "%v", "playlist.m3u8")),
	}

	args := []string{
		"run", "--rm",
		"--network", "none",
		"--cpus=4.0",
		"--memory=4g",
		"-v", fmt.Sprintf("%s:/work", mountDir),
		"-w", "/work",
		t.dockerImage,
		"-nostdin",
		"-y",
		"-protocol_whitelist", "file,crypto,data",
		"-probesize", "10000000",
		"-analyzeduration", "10000000",
		"-i", relSource,
		"-filter_complex", filterComplex,
	}
	args = append(args, mapsAndCodecs...)
	args = append(args, hlsArgs...)

	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg ABR transcoding failed: %w, log: %s", err, string(out))
	}

	// Verify generated segments count for each rendition
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

// ExtractThumbnail captures a video frame poster at the specified second
func (t *DockerFFmpegTranscoder) ExtractThumbnail(ctx context.Context, localSourcePath, outputPath string, atSecond float64) error {
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

	mountDir, relPaths, err := resolveMountAndPaths(localSourcePath, outputPath)
	if err != nil {
		return fmt.Errorf("failed to resolve mount paths: %w", err)
	}
	relSource := relPaths[0]
	relOutput := relPaths[1]

	args := []string{
		"run", "--rm",
		"--network", "none",
		"-v", fmt.Sprintf("%s:/work", mountDir),
		"-w", "/work",
		t.dockerImage,
		"-nostdin",
		"-y",
		"-ss", fmt.Sprintf("%.2f", atSecond),
		"-i", relSource,
		"-frames:v", "1",
		"-update", "1",
		"-q:v", "2",
		relOutput,
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("thumbnail extraction failed: %w, log: %s", err, string(out))
	}

	return nil
}

// TranscodeToHLS implements legacy single-bitrate interface for backward compatibility
func (t *DockerFFmpegTranscoder) TranscodeToHLS(ctx context.Context, sourcePath, outputDir, playlistName string) error {
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

	mountDir, relPaths, err := resolveMountAndPaths(sourcePath, outputDir)
	if err != nil {
		return err
	}
	relSource := relPaths[0]
	relOutput := relPaths[1]

	playlistTarget := filepath.Join(relOutput, playlistName)
	segmentPattern := filepath.Join(relOutput, "segment_%03d.ts")

	args := []string{
		"run", "--rm",
		"-v", fmt.Sprintf("%s:/work", mountDir),
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
		return fmt.Errorf("ffmpeg error: %w, log: %s", err, string(out))
	}

	return nil
}

// resolveMountAndPaths finds the common base directory for given host paths and returns relative paths
func resolveMountAndPaths(paths ...string) (string, []string, error) {
	if len(paths) == 0 {
		return "", nil, errors.New("no paths provided")
	}

	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			return "", nil, errors.New("cannot resolve mount paths: path cannot be empty")
		}
	}

	absPaths := make([]string, len(paths))
	for i, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", nil, fmt.Errorf("failed to get abs path for %s: %w", p, err)
		}
		absPaths[i] = abs
	}

	// Find common ancestor directory
	commonDir := filepath.Dir(absPaths[0])
	for _, p := range absPaths[1:] {
		for !strings.HasPrefix(p, commonDir) {
			parent := filepath.Dir(commonDir)
			if parent == commonDir {
				break
			}
			commonDir = parent
		}
	}

	relPaths := make([]string, len(absPaths))
	for i, p := range absPaths {
		rel, err := filepath.Rel(commonDir, p)
		if err != nil {
			return "", nil, fmt.Errorf("failed to get rel path: %w", err)
		}
		relPaths[i] = filepath.ToSlash(rel)
	}

	return commonDir, relPaths, nil
}
