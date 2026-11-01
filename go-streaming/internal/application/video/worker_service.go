package video

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quidb.dev/go-streaming/internal/domain/video"
	"quidb.dev/go-streaming/pkg/logger"
	"quidb.dev/go-streaming/pkg/metrics"
)

// CloudTranscodeDispatcher defines the contract for submitting serverless transcoding jobs (e.g. GCP Transcoder API)
type CloudTranscodeDispatcher interface {
	SubmitJob(ctx context.Context, videoID, sourceBucket, sourceKey, targetBucket, targetPrefix string, hasAudio bool) (jobName string, err error)
	GetJobState(ctx context.Context, jobName string) (video.CloudJobState, error)
}

// WorkerService orchestrates the asynchronous video transcoding pipeline
type WorkerService struct {
	videoRepo       video.Repository
	jobRepo         video.JobRepository
	renditionRepo   video.RenditionRepository
	storageProvider video.StorageProvider
	transcoder      video.Transcoder
	cloudTranscoder CloudTranscodeDispatcher
	jobQueue        video.JobQueue
	scratchBaseDir  string
	publicDomain    string
	workerID        string
	log             *logger.Logger
}

func NewWorkerService(
	videoRepo video.Repository,
	jobRepo video.JobRepository,
	renditionRepo video.RenditionRepository,
	storageProvider video.StorageProvider,
	transcoder video.Transcoder,
	jobQueue video.JobQueue,
	scratchBaseDir string,
	publicDomain string,
	workerID string,
	log *logger.Logger,
) *WorkerService {
	if scratchBaseDir == "" {
		scratchBaseDir = "/tmp/bitmill/transcode"
	}
	if workerID == "" {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "worker-default"
		}
		workerID = hostname
	}
	publicDomain = strings.TrimSuffix(publicDomain, "/")

	return &WorkerService{
		videoRepo:       videoRepo,
		jobRepo:         jobRepo,
		renditionRepo:   renditionRepo,
		storageProvider: storageProvider,
		transcoder:      transcoder,
		jobQueue:        jobQueue,
		scratchBaseDir:  scratchBaseDir,
		publicDomain:    publicDomain,
		workerID:        workerID,
		log:             log,
	}
}

func (s *WorkerService) SetCloudTranscoder(ct CloudTranscodeDispatcher) {
	s.cloudTranscoder = ct
}

// StartConsumer begins consuming transcoding messages from RabbitMQ until context is cancelled
func (s *WorkerService) StartConsumer(ctx context.Context) error {
	s.log.Info("Starting Transcoding Worker Daemon (WorkerID=%s, ScratchDir=%s)", s.workerID, s.scratchBaseDir)
	return s.jobQueue.ConsumeTranscodeJobs(ctx, s.ProcessJob)
}

// ProcessJob executes the end-to-end media transcoding workflow for a single job
func (s *WorkerService) ProcessJob(ctx context.Context, msg *video.TranscodeJobMessage) error {
	startTime := time.Now()
	s.log.Info("Executing transcode job: VideoID=%s, JobID=%s, Attempt=%d/%d",
		msg.VideoID, msg.JobID, msg.AttemptCount, msg.MaxAttempts)

	// 1. Idempotency & State Transition Check
	v, err := s.videoRepo.FindByID(ctx, msg.VideoID)
	if err != nil {
		s.log.Error("Video not found in repository for VideoID=%s: %v", msg.VideoID, err)
		return video.ErrVideoNotFound
	}

	if v.Status == video.StatusReady {
		s.log.Info("Video %s is already in READY state, acknowledging duplicate event without reprocessing", msg.VideoID)
		return nil
	}

	// Update Video Status to PROCESSING
	_ = s.videoRepo.UpdateStatus(ctx, msg.VideoID, video.StatusProcessing)
	if s.jobRepo != nil && msg.JobID != "" {
		_ = s.jobRepo.MarkJobProcessing(ctx, msg.JobID, s.workerID)
	}

	// 1.1 Serverless Cloud Transcoder Delegation (Zero CPU VPS Processing)
	if s.cloudTranscoder != nil {
		s.log.Info("Delegating video transcoding to Serverless Cloud Transcoder for VideoID=%s...", msg.VideoID)

		hasAudio := s.detectAudio(ctx, msg.SourceBucket, msg.SourceKey)
		s.log.Info("Audio stream inspection for VideoID=%s: hasAudio=%v", msg.VideoID, hasAudio)

		jobName, err := s.cloudTranscoder.SubmitJob(ctx, msg.VideoID, msg.SourceBucket, msg.SourceKey, msg.TargetBucket, msg.TargetPrefix, hasAudio)
		if err != nil {
			s.log.Error("Failed to submit Serverless Cloud Transcoder job for VideoID=%s: %v", msg.VideoID, err)
			_ = s.videoRepo.MarkFailed(ctx, msg.VideoID)
			if s.jobRepo != nil && msg.JobID != "" {
				_ = s.jobRepo.MarkJobFailed(ctx, msg.JobID, err.Error())
			}
			return fmt.Errorf("cloud transcoder submission failed: %w", err)
		}
		s.log.Info("Serverless Transcoder Job scheduled successfully: %s", jobName)

		// Start background job watcher with self-healing audio fallback
		go s.monitorCloudTranscoderJob(jobName, msg.VideoID, msg.SourceBucket, msg.SourceKey, msg.TargetBucket, msg.TargetPrefix)
		return nil
	}

	// 2. Setup Isolated Scratch Directory (Docker / Native Local Fallback)
	jobScratchDir := filepath.Join(s.scratchBaseDir, msg.JobID)
	if err := os.MkdirAll(jobScratchDir, 0755); err != nil {
		s.log.Error("Failed to create scratch directory %s: %v", jobScratchDir, err)
		return fmt.Errorf("scratch dir creation failed: %w", err)
	}
	defer func() {
		s.log.Info("Purging scratch directory: %s", jobScratchDir)
		_ = os.RemoveAll(jobScratchDir)
	}()

	// 3. Download Source Video from Cloudflare R2
	localSource := filepath.Join(jobScratchDir, "source.mp4")
	s.log.Info("Downloading source from R2 bucket %s (key=%s)...", msg.SourceBucket, msg.SourceKey)
	if err := s.storageProvider.DownloadToFile(ctx, msg.SourceBucket, msg.SourceKey, localSource); err != nil {
		s.log.Error("Failed to download source video from R2: %v", err)
		return fmt.Errorf("source download failed: %w", err)
	}

	// 4. Media Inspection & Pre-Validation via ffprobe
	s.log.Info("Probing source media metadata via Docker ffprobe...")
	meta, err := s.transcoder.ProbeMedia(ctx, localSource)
	if err != nil {
		s.log.Error("Poison pill media detected or unparsable container for VideoID=%s: %v", msg.VideoID, err)
		_ = s.videoRepo.MarkFailed(ctx, msg.VideoID)
		if s.jobRepo != nil && msg.JobID != "" {
			_ = s.jobRepo.MarkJobDLQ(ctx, msg.JobID, fmt.Sprintf("poison pill media: %v", err))
		}
		// Return permanent error so consumer sends message straight to DLQ
		return fmt.Errorf("permanent invalid format: %w", err)
	}
	s.log.Info("Source media verified: Dimensions=%dx%d, Duration=%.2fs, VideoCodec=%s, AudioCodec=%s",
		meta.Width, meta.Height, meta.DurationSeconds, meta.VideoCodec, meta.AudioCodec)

	// 5. Calculate Dynamic ABR Ladder (Input Resolution Capping)
	profiles := video.SelectRenditions(meta.Height)
	var profileNames []string
	for _, p := range profiles {
		profileNames = append(profileNames, p.Name)
	}
	s.log.Info("Generated dynamic resolution ladder for %dp source: [%s]", meta.Height, strings.Join(profileNames, ", "))

	abrCfg := video.ABRConfig{
		Renditions:      profiles,
		GOPSize:         60,
		SegmentDuration: 2,
	}

	// 6. Execute Single-Pass Multi-Output FFmpeg Transcoding
	hlsOutputDir := filepath.Join(jobScratchDir, "hls")
	s.log.Info("Starting Docker FFmpeg ABR transcoding into %s...", hlsOutputDir)
	renditions, err := s.transcoder.TranscodeABR(ctx, localSource, hlsOutputDir, abrCfg)
	if err != nil {
		s.log.Error("FFmpeg ABR transcoding failed for VideoID=%s: %v", msg.VideoID, err)
		if s.jobRepo != nil && msg.JobID != "" {
			_ = s.jobRepo.MarkJobFailed(ctx, msg.JobID, err.Error())
		}
		return fmt.Errorf("transcode ABR error: %w", err)
	}

	// 7. Extract Thumbnail Poster
	thumbPath := filepath.Join(hlsOutputDir, "thumbnail.jpg")
	atSecond := 1.0
	if meta.DurationSeconds > 3.0 {
		atSecond = 2.0
	}
	s.log.Info("Extracting thumbnail poster at %.1fs...", atSecond)
	if thumbErr := s.transcoder.ExtractThumbnail(ctx, localSource, thumbPath, atSecond); thumbErr != nil {
		s.log.Warn("Failed to extract thumbnail poster, continuing: %v", thumbErr)
	}

	// 8. Upload HLS Multi-Rendition Package to Cloudflare R2
	s.log.Info("Uploading HLS package to Cloudflare R2 bucket %s (prefix=%s)...", msg.TargetBucket, msg.TargetPrefix)
	if err := s.storageProvider.UploadDirectory(ctx, hlsOutputDir, msg.TargetBucket, msg.TargetPrefix); err != nil {
		s.log.Error("Failed to upload HLS directory to R2: %v", err)
		return fmt.Errorf("r2 upload directory failed: %w", err)
	}

	// 9. Atomic Database State & Metadata Finalization
	if s.renditionRepo != nil {
		if err := s.renditionRepo.SaveRenditions(ctx, msg.VideoID, renditions); err != nil {
			s.log.Error("Failed to persist video renditions to database: %v", err)
			return fmt.Errorf("failed to save renditions: %w", err)
		}
	}

	var masterURL, thumbURL string
	if s.publicDomain != "" {
		masterURL = fmt.Sprintf("%s/%s/master.m3u8", s.publicDomain, msg.TargetPrefix)
		thumbURL = fmt.Sprintf("%s/%s/thumbnail.jpg", s.publicDomain, msg.TargetPrefix)
	} else {
		masterURL = fmt.Sprintf("https://%s.r2.cloudflarestorage.com/%s/%s/master.m3u8", msg.TargetBucket, msg.TargetBucket, msg.TargetPrefix)
		thumbURL = fmt.Sprintf("https://%s.r2.cloudflarestorage.com/%s/%s/thumbnail.jpg", msg.TargetBucket, msg.TargetBucket, msg.TargetPrefix)
	}

	if err := s.videoRepo.MarkReady(ctx, msg.VideoID, masterURL, thumbURL, meta.DurationSeconds); err != nil {
		s.log.Error("Failed to mark video as READY in DB: %v", err)
		return fmt.Errorf("failed to mark video ready: %w", err)
	}

	if s.jobRepo != nil && msg.JobID != "" {
		_ = s.jobRepo.MarkJobCompleted(ctx, msg.JobID)
	}

	// Record Prometheus Telemetry Metrics
	metrics.TranscodeJobsTotal.WithLabelValues("success", fmt.Sprintf("%dp", meta.Height)).Inc()
	metrics.TranscodeDuration.WithLabelValues(fmt.Sprintf("%dp", meta.Height)).Observe(time.Since(startTime).Seconds())

	s.log.Info("Transcoding pipeline completed successfully for VideoID=%s! Master URL: %s", msg.VideoID, masterURL)
	return nil
}

// detectAudio probes the source media container via partial range request to check for audio tracks
func (s *WorkerService) detectAudio(ctx context.Context, bucket, key string) bool {
	if s.storageProvider == nil {
		return true
	}

	dlURL, err := s.storageProvider.GetDownloadURL(ctx, bucket, key, 5*time.Minute)
	if err != nil {
		s.log.Warn("Failed to obtain download URL for audio inspection on %s/%s: %v", bucket, key, err)
		return true
	}

	fetchRange := func(rangeHeader string) []byte {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
		if err != nil {
			return nil
		}
		req.Header.Set("Range", rangeHeader)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()

		buf, _ := io.ReadAll(resp.Body)
		return buf
	}

	// 1. Check head of file (512KB) for faststart/web-optimized moov atom
	headBuf := fetchRange("bytes=0-524288")
	if len(headBuf) > 0 {
		if bytes.Contains(headBuf, []byte("soun")) {
			return true
		}
		if bytes.Contains(headBuf, []byte("moov")) && bytes.Contains(headBuf, []byte("vide")) {
			s.log.Info("Audio Detection: detected silent/video-only container (no 'soun' track in head moov atom)")
			return false
		}
	}

	// 2. Check tail of file (512KB) for standard non-faststart moov atom placed after mdat
	fileSize, err := s.storageProvider.GetObjectSize(ctx, bucket, key)
	if err == nil && fileSize > 0 {
		rangeStart := int64(0)
		if fileSize > 524288 {
			rangeStart = fileSize - 524288
		}
		tailRange := fmt.Sprintf("bytes=%d-%d", rangeStart, fileSize-1)
		tailBuf := fetchRange(tailRange)
		if len(tailBuf) > 0 {
			if bytes.Contains(tailBuf, []byte("soun")) {
				return true
			}
			if bytes.Contains(tailBuf, []byte("moov")) && bytes.Contains(tailBuf, []byte("vide")) {
				s.log.Info("Audio Detection: detected silent/video-only container (no 'soun' track in tail moov atom)")
				return false
			}
		}
	}

	return true
}

func (s *WorkerService) monitorCloudTranscoderJob(jobName, videoID, sourceBucket, sourceKey, targetBucket, targetPrefix string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	hasAutoRetriedSilent := false

	for {
		select {
		case <-ctx.Done():
			s.log.Warn("Background watcher timed out for GCP job %s (VideoID=%s)", jobName, videoID)
			return
		case <-ticker.C:
			state, err := s.cloudTranscoder.GetJobState(ctx, jobName)
			if err != nil {
				s.log.Warn("Failed to poll GCP job state for %s: %v", jobName, err)
				continue
			}

			switch state.State {
			case "SUCCEEDED":
				s.log.Info("🎉 GCP Transcoder Job %s SUCCEEDED for VideoID=%s! Finalizing video and renditions...", jobName, videoID)
				s.finalizeVideoFromGCP(ctx, videoID, targetBucket, targetPrefix)
				return
			case "FAILED":
				// Self-Healing: If GCP Transcoder failed due to AudioMissing on a silent video, auto-resubmit with hasAudio=false!
				lowerErr := strings.ToLower(state.ErrorMessage)
				if !hasAutoRetriedSilent && (strings.Contains(lowerErr, "audio") || strings.Contains(lowerErr, "audiomissing")) {
					hasAutoRetriedSilent = true
					s.log.Warn("⚠️ GCP Transcoder reported AudioMissing error for VideoID=%s. Auto-healing: re-submitting with hasAudio=false...", videoID)
					retryJobName, err := s.cloudTranscoder.SubmitJob(ctx, videoID, sourceBucket, sourceKey, targetBucket, targetPrefix, false)
					if err == nil {
						s.log.Info("🔄 Successfully re-submitted auto-healed silent video job: %s. Resuming monitoring...", retryJobName)
						jobName = retryJobName
						continue
					}
				}

				s.log.Error("❌ GCP Transcoder Job %s FAILED for VideoID=%s: %s", jobName, videoID, state.ErrorMessage)
				_ = s.videoRepo.MarkFailed(ctx, videoID)
				if s.jobRepo != nil {
					_ = s.jobRepo.MarkJobFailed(ctx, videoID, state.ErrorMessage)
				}
				return
			}
		}
	}
}

func (s *WorkerService) finalizeVideoFromGCP(ctx context.Context, videoID, targetBucket, targetPrefix string) {
	prefix := strings.Trim(targetPrefix, "/")
	masterURL := fmt.Sprintf("https://storage.googleapis.com/%s/%s/master.m3u8", targetBucket, prefix)
	thumbnailURL := fmt.Sprintf("https://storage.googleapis.com/%s/%s/thumbnail0000000000.jpeg", targetBucket, prefix)

	renditions := []video.Rendition{
		{
			ResolutionName:   "1080p",
			Width:            1920,
			Height:           1080,
			VideoBitrateKbps: 4500,
			AudioBitrateKbps: 128,
			PlaylistPath:     fmt.Sprintf("%s/1080p.m3u8", prefix),
			Status:           "READY",
		},
		{
			ResolutionName:   "720p",
			Width:            1280,
			Height:           720,
			VideoBitrateKbps: 2500,
			AudioBitrateKbps: 128,
			PlaylistPath:     fmt.Sprintf("%s/720p.m3u8", prefix),
			Status:           "READY",
		},
		{
			ResolutionName:   "480p",
			Width:            854,
			Height:           480,
			VideoBitrateKbps: 1000,
			AudioBitrateKbps: 128,
			PlaylistPath:     fmt.Sprintf("%s/480p.m3u8", prefix),
			Status:           "READY",
		},
		{
			ResolutionName:   "360p",
			Width:            640,
			Height:           360,
			VideoBitrateKbps: 600,
			AudioBitrateKbps: 128,
			PlaylistPath:     fmt.Sprintf("%s/360p.m3u8", prefix),
			Status:           "READY",
		},
	}

	if s.renditionRepo != nil {
		_ = s.renditionRepo.SaveRenditions(ctx, videoID, renditions)
	}
	_ = s.videoRepo.MarkReady(ctx, videoID, masterURL, thumbnailURL, 0)
	s.log.Info("Video %s is now READY on GCS! Master Playlist: %s", videoID, masterURL)
}
