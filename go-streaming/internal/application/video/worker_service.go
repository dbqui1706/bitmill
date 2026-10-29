package video

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quidb.dev/go-streaming/internal/domain/video"
	"quidb.dev/go-streaming/pkg/logger"
	"quidb.dev/go-streaming/pkg/metrics"
)

// WorkerService orchestrates the asynchronous video transcoding pipeline
type WorkerService struct {
	videoRepo       video.Repository
	jobRepo         video.JobRepository
	renditionRepo   video.RenditionRepository
	storageProvider video.StorageProvider
	transcoder      video.Transcoder
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

	// 2. Setup Isolated Scratch Directory
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
