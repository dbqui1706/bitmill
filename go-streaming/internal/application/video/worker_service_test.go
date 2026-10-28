package video_test

import (
	"context"
	"errors"
	"testing"
	"time"

	videoApp "quidb.dev/go-streaming/internal/application/video"
	"quidb.dev/go-streaming/internal/domain/video"
	"quidb.dev/go-streaming/pkg/logger"
)

type mockJobRepo struct {
	jobs map[string]*video.ProcessingJob
}

func newMockJobRepo() *mockJobRepo {
	return &mockJobRepo{jobs: make(map[string]*video.ProcessingJob)}
}

func (m *mockJobRepo) CreateJob(ctx context.Context, videoID, workerID string, maxAttempts int) (*video.ProcessingJob, error) {
	job := &video.ProcessingJob{
		ID:          "job-uuid-" + videoID,
		VideoID:     videoID,
		WorkerID:    workerID,
		Status:      video.JobStatusQueued,
		MaxAttempts: maxAttempts,
		CreatedAt:   time.Now(),
	}
	m.jobs[job.ID] = job
	return job, nil
}

func (m *mockJobRepo) MarkJobProcessing(ctx context.Context, jobID, workerID string) error {
	if j, ok := m.jobs[jobID]; ok {
		j.Status = video.JobStatusProcessing
		j.WorkerID = workerID
	}
	return nil
}

func (m *mockJobRepo) MarkJobCompleted(ctx context.Context, jobID string) error {
	if j, ok := m.jobs[jobID]; ok {
		j.Status = video.JobStatusCompleted
	}
	return nil
}

func (m *mockJobRepo) MarkJobFailed(ctx context.Context, jobID, errorMessage string) error {
	if j, ok := m.jobs[jobID]; ok {
		j.Status = video.JobStatusFailed
		j.ErrorMessage = errorMessage
	}
	return nil
}

func (m *mockJobRepo) MarkJobDLQ(ctx context.Context, jobID, errorMessage string) error {
	if j, ok := m.jobs[jobID]; ok {
		j.Status = video.JobStatusDLQ
		j.ErrorMessage = errorMessage
	}
	return nil
}

func (m *mockJobRepo) GetActiveJobByVideoID(ctx context.Context, videoID string) (*video.ProcessingJob, error) {
	for _, j := range m.jobs {
		if j.VideoID == videoID {
			return j, nil
		}
	}
	return nil, nil
}

func (m *mockJobRepo) GetJobByID(ctx context.Context, jobID string) (*video.ProcessingJob, error) {
	if j, ok := m.jobs[jobID]; ok {
		return j, nil
	}
	return nil, nil
}

type mockRenditionRepo struct {
	renditions map[string][]video.Rendition
}

func newMockRenditionRepo() *mockRenditionRepo {
	return &mockRenditionRepo{renditions: make(map[string][]video.Rendition)}
}

func (m *mockRenditionRepo) SaveRenditions(ctx context.Context, videoID string, rends []video.Rendition) error {
	m.renditions[videoID] = rends
	return nil
}

func (m *mockRenditionRepo) ListRenditions(ctx context.Context, videoID string) ([]video.Rendition, error) {
	return m.renditions[videoID], nil
}

type mockTranscoder struct {
	probeFunc            func(ctx context.Context, localSourcePath string) (*video.MediaMetadata, error)
	transcodeABRFunc     func(ctx context.Context, localSourcePath, outputDir string, cfg video.ABRConfig) ([]video.Rendition, error)
	extractThumbnailFunc func(ctx context.Context, localSourcePath, outputPath string, atSecond float64) error
}

func (m *mockTranscoder) TranscodeToHLS(ctx context.Context, sourcePath, outputDir, playlistName string) error {
	return nil
}

func (m *mockTranscoder) ProbeMedia(ctx context.Context, localSourcePath string) (*video.MediaMetadata, error) {
	if m.probeFunc != nil {
		return m.probeFunc(ctx, localSourcePath)
	}
	return &video.MediaMetadata{
		DurationSeconds: 120.0,
		Width:           1920,
		Height:          1080,
		VideoCodec:      "h264",
		AudioCodec:      "aac",
		BitrateKbps:     4500,
	}, nil
}

func (m *mockTranscoder) TranscodeABR(ctx context.Context, localSourcePath, outputDir string, cfg video.ABRConfig) ([]video.Rendition, error) {
	if m.transcodeABRFunc != nil {
		return m.transcodeABRFunc(ctx, localSourcePath, outputDir, cfg)
	}
	var res []video.Rendition
	for _, p := range cfg.Renditions {
		res = append(res, video.Rendition{
			ResolutionName:   p.Name,
			Width:            p.Width,
			Height:           p.Height,
			VideoBitrateKbps: p.VideoBitrateKbps,
			AudioBitrateKbps: p.AudioBitrateKbps,
			PlaylistPath:     p.Name + "/playlist.m3u8",
			SegmentCount:     60,
			Status:           "READY",
		})
	}
	return res, nil
}

func (m *mockTranscoder) ExtractThumbnail(ctx context.Context, localSourcePath, outputPath string, atSecond float64) error {
	if m.extractThumbnailFunc != nil {
		return m.extractThumbnailFunc(ctx, localSourcePath, outputPath, atSecond)
	}
	return nil
}

type mockJobQueue struct {
	published []*video.TranscodeJobMessage
}

func (m *mockJobQueue) PublishTranscodeJob(ctx context.Context, job *video.TranscodeJobMessage) error {
	m.published = append(m.published, job)
	return nil
}

func (m *mockJobQueue) ConsumeTranscodeJobs(ctx context.Context, handler func(ctx context.Context, job *video.TranscodeJobMessage) error) error {
	return nil
}

func (m *mockJobQueue) Close() error {
	return nil
}

// -------------------------------------------------------------
// Test Cases
// -------------------------------------------------------------

func TestDynamicResolutionLadder_InputCapping(t *testing.T) {
	tests := []struct {
		name         string
		sourceHeight int
		expected     []string
	}{
		{
			name:         "4K Source (2160p) yields all 6 variants",
			sourceHeight: 2160,
			expected:     []string{"4k", "1440p", "1080p", "720p", "480p", "360p"},
		},
		{
			name:         "1080p Source yields 1080p and below (never upscales to 4K or 1440p)",
			sourceHeight: 1080,
			expected:     []string{"1080p", "720p", "480p", "360p"},
		},
		{
			name:         "720p Source yields 720p, 480p, 360p",
			sourceHeight: 720,
			expected:     []string{"720p", "480p", "360p"},
		},
		{
			name:         "480p Source yields 480p, 360p",
			sourceHeight: 480,
			expected:     []string{"480p", "360p"},
		},
		{
			name:         "Low resolution source (<360p) yields baseline 360p",
			sourceHeight: 240,
			expected:     []string{"360p"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			renditions := video.SelectRenditions(tt.sourceHeight)
			if len(renditions) != len(tt.expected) {
				t.Fatalf("expected %d renditions, got %d", len(tt.expected), len(renditions))
			}
			for i, exp := range tt.expected {
				if renditions[i].Name != exp {
					t.Errorf("at index %d: expected %s, got %s", i, exp, renditions[i].Name)
				}
			}
		})
	}
}

func TestWorkerService_ProcessJob_HappyPath(t *testing.T) {
	ctx := context.Background()
	vRepo := newMockVideoRepo()
	jRepo := newMockJobRepo()
	rRepo := newMockRenditionRepo()
	storage := &mockStorageProvider{}
	transcoder := &mockTranscoder{}
	queue := &mockJobQueue{}

	videoID := "vid-12345"
	v, _ := video.NewVideo(videoID, "Test Video", "source.mp4")
	v.Status = video.StatusUploaded
	_ = vRepo.Save(ctx, v)
	_, _ = jRepo.CreateJob(ctx, videoID, "worker-1", 3)
	jRepo.jobs["job-1"] = &video.ProcessingJob{ID: "job-1", VideoID: videoID, Status: video.JobStatusQueued}

	worker := videoApp.NewWorkerService(
		vRepo, jRepo, rRepo, storage, transcoder, queue,
		"/tmp/test_bitmill", "https://pub.bitmill.dev", "test-worker", logger.New(),
	)

	jobMsg := &video.TranscodeJobMessage{
		EventID:      "evt-1",
		EventType:    "video.uploaded",
		VideoID:      videoID,
		JobID:        "job-1",
		SourceBucket: "bitmill-raw-videos",
		SourceKey:    "raw/vid-12345/source.mp4",
		TargetBucket: "bitmill-processed-videos",
		TargetPrefix: "hls/vid-12345",
		AttemptCount: 1,
		MaxAttempts:  3,
	}

	err := worker.ProcessJob(ctx, jobMsg)
	if err != nil {
		t.Fatalf("unexpected error processing job: %v", err)
	}

	// Verify video updated to READY
	updatedVideo, err := vRepo.FindByID(ctx, videoID)
	if err != nil {
		t.Fatalf("failed to find updated video: %v", err)
	}
	if updatedVideo.Status != video.StatusReady {
		t.Errorf("expected video status READY, got %s", updatedVideo.Status)
	}
	if updatedVideo.MasterPlaylistURL != "https://pub.bitmill.dev/hls/vid-12345/master.m3u8" {
		t.Errorf("unexpected master URL: %s", updatedVideo.MasterPlaylistURL)
	}

	// Verify job marked completed
	job, _ := jRepo.GetJobByID(ctx, "job-1")
	if job == nil || job.Status != video.JobStatusCompleted {
		t.Errorf("expected job status COMPLETED, got %v", job)
	}
}

func TestWorkerService_ProcessJob_PoisonPill(t *testing.T) {
	ctx := context.Background()
	vRepo := newMockVideoRepo()
	jRepo := newMockJobRepo()
	rRepo := newMockRenditionRepo()
	storage := &mockStorageProvider{}
	transcoder := &mockTranscoder{
		probeFunc: func(ctx context.Context, localSourcePath string) (*video.MediaMetadata, error) {
			return nil, errors.New("moov atom not found: invalid container")
		},
	}
	queue := &mockJobQueue{}

	videoID := "vid-poison"
	v, _ := video.NewVideo(videoID, "Corrupt Video", "corrupt.mp4")
	v.Status = video.StatusUploaded
	_ = vRepo.Save(ctx, v)
	jRepo.jobs["job-poison"] = &video.ProcessingJob{ID: "job-poison", VideoID: videoID, Status: video.JobStatusQueued}

	worker := videoApp.NewWorkerService(
		vRepo, jRepo, rRepo, storage, transcoder, queue,
		"/tmp/test_bitmill", "https://pub.bitmill.dev", "test-worker", logger.New(),
	)

	jobMsg := &video.TranscodeJobMessage{
		EventID:      "evt-poison",
		VideoID:      videoID,
		JobID:        "job-poison",
		SourceBucket: "bitmill-raw-videos",
		SourceKey:    "raw/vid-poison/corrupt.mp4",
		TargetBucket: "bitmill-processed-videos",
		TargetPrefix: "hls/vid-poison",
		AttemptCount: 1,
		MaxAttempts:  3,
	}

	err := worker.ProcessJob(ctx, jobMsg)
	if err == nil {
		t.Fatal("expected error on poison pill media, got nil")
	}

	// Verify video marked FAILED
	updatedVideo, _ := vRepo.FindByID(ctx, videoID)
	if updatedVideo.Status != video.StatusFailed {
		t.Errorf("expected video status FAILED, got %s", updatedVideo.Status)
	}

	// Verify job quarantined to DLQ
	job, _ := jRepo.GetJobByID(ctx, "job-poison")
	if job == nil || job.Status != video.JobStatusDLQ {
		t.Errorf("expected job status DLQ, got %v", job)
	}
}

func TestWorkerService_ProcessJob_Idempotency(t *testing.T) {
	ctx := context.Background()
	vRepo := newMockVideoRepo()
	jRepo := newMockJobRepo()
	rRepo := newMockRenditionRepo()
	storage := &mockStorageProvider{}
	transcoder := &mockTranscoder{}
	queue := &mockJobQueue{}

	videoID := "vid-already-ready"
	v, _ := video.NewVideo(videoID, "Ready Video", "ready.mp4")
	v.Status = video.StatusReady
	_ = vRepo.Save(ctx, v)

	worker := videoApp.NewWorkerService(
		vRepo, jRepo, rRepo, storage, transcoder, queue,
		"/tmp/test_bitmill", "https://pub.bitmill.dev", "test-worker", logger.New(),
	)

	jobMsg := &video.TranscodeJobMessage{
		EventID: "evt-dup",
		VideoID: videoID,
		JobID:   "job-dup",
	}

	err := worker.ProcessJob(ctx, jobMsg)
	if err != nil {
		t.Fatalf("expected nil error for duplicate READY video, got: %v", err)
	}
}

func TestUploadService_DispatchesRabbitMQJob(t *testing.T) {
	ctx := context.Background()
	vRepo := newMockVideoRepo()
	sRepo := newMockSessionRepo()
	storage := &mockStorageProvider{}
	jRepo := newMockJobRepo()
	queue := &mockJobQueue{}

	videoID := "vid-queue-test"
	v, _ := video.NewVideo(videoID, "Queue Test", "test.mp4")
	_ = vRepo.Save(ctx, v)

	svc := videoApp.NewUploadService(vRepo, sRepo, storage, "bitmill-raw-videos", time.Hour, logger.New())
	svc.SetTranscodingQueue(jRepo, queue, "bitmill-processed-videos")

	// Create session
	sessResp, err := svc.CreateUploadSession(ctx, videoApp.CreateSessionRequest{
		UserID:   "usr-1",
		Title:    "Queue Test",
		FileName: "test.mp4",
		FileSize: 10485760, // 10MB
		MimeType: "video/mp4",
	})
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Complete upload
	completeResp, err := svc.CompleteUpload(ctx, sessResp.VideoID, []video.CompletedPart{
		{PartNumber: 1, ETag: "etag-1"},
	})
	if err != nil {
		t.Fatalf("failed to complete upload: %v", err)
	}

	if completeResp.Status != string(video.StatusUploaded) {
		t.Errorf("expected status UPLOADED, got %s", completeResp.Status)
	}

	// Verify job was published to RabbitMQ
	if len(queue.published) != 1 {
		t.Fatalf("expected 1 job published to queue, got %d", len(queue.published))
	}
	publishedMsg := queue.published[0]
	if publishedMsg.VideoID != sessResp.VideoID {
		t.Errorf("expected VideoID %s in message, got %s", sessResp.VideoID, publishedMsg.VideoID)
	}
	if publishedMsg.TargetBucket != "bitmill-processed-videos" {
		t.Errorf("expected TargetBucket bitmill-processed-videos, got %s", publishedMsg.TargetBucket)
	}
	if publishedMsg.TargetPrefix != "hls/"+sessResp.VideoID {
		t.Errorf("expected TargetPrefix hls/%s, got %s", sessResp.VideoID, publishedMsg.TargetPrefix)
	}
}
