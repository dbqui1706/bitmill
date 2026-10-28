package video

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"quidb.dev/go-streaming/internal/domain/video"
	"quidb.dev/go-streaming/pkg/logger"
)

var (
	ErrVideoNotFound      = errors.New("video not found")
	ErrSessionNotFound    = errors.New("upload session not found")
	ErrSessionExpired     = errors.New("upload session has expired")
	ErrInvalidPartCount   = errors.New("uploaded parts count does not match total parts")
	ErrInvalidContentType = errors.New("invalid content type, must be a video format")
)

type CreateSessionRequest struct {
	UserID          string `json:"user_id"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	FileName        string `json:"file_name"`
	FileSize        int64  `json:"file_size"`
	MimeType        string `json:"mime_type"`
	FileFingerprint string `json:"file_fingerprint"`
}

type CreateSessionResponse struct {
	VideoID         string                   `json:"video_id"`
	UploadID        string                   `json:"upload_id"`
	PartSizeBytes   int64                    `json:"part_size_bytes"`
	TotalParts      int                      `json:"total_parts"`
	ExpiresAt       time.Time                `json:"expires_at"`
	Parts           []video.PartPresignedURL `json:"parts"`
	IsResumed       bool                     `json:"is_resumed"`
}

type UploadStatusResponse struct {
	VideoID             string                   `json:"video_id"`
	Status              string                   `json:"status"`
	TotalParts          int                      `json:"total_parts"`
	CompletedPartsCount int                      `json:"completed_parts_count"`
	CompletedParts      []video.CompletedPart    `json:"completed_parts"`
	MissingParts        []video.PartPresignedURL `json:"missing_parts"`
}

type CompleteUploadResponse struct {
	VideoID string `json:"video_id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type UploadService struct {
	videoRepo       video.Repository
	sessionRepo     video.UploadSessionRepository
	storageProvider video.StorageProvider
	jobRepo         video.JobRepository // Milestone 4
	jobQueue        video.JobQueue      // Milestone 4
	rawBucket       string
	processedBucket string              // Milestone 4
	sessionTTL      time.Duration
	log             *logger.Logger
}

func NewUploadService(
	videoRepo video.Repository,
	sessionRepo video.UploadSessionRepository,
	storageProvider video.StorageProvider,
	rawBucket string,
	sessionTTL time.Duration,
	log *logger.Logger,
) *UploadService {
	if sessionTTL <= 0 {
		sessionTTL = 2 * time.Hour
	}
	return &UploadService{
		videoRepo:       videoRepo,
		sessionRepo:     sessionRepo,
		storageProvider: storageProvider,
		rawBucket:       rawBucket,
		sessionTTL:      sessionTTL,
		log:             log,
	}
}

// SetTranscodingQueue wires the RabbitMQ orchestration broker and job repository (Milestone 4)
func (s *UploadService) SetTranscodingQueue(jobRepo video.JobRepository, jobQueue video.JobQueue, processedBucket string) {
	s.jobRepo = jobRepo
	s.jobQueue = jobQueue
	s.processedBucket = processedBucket
}

// CreateUploadSession initializes a resumable multipart session on Cloudflare R2
func (s *UploadService) CreateUploadSession(ctx context.Context, req CreateSessionRequest) (*CreateSessionResponse, error) {
	// 1. Boundary & Invariant Validations
	if req.FileSize < video.MinPartSizeBytes || req.FileSize > video.MaxFileSizeBytes {
		return nil, fmt.Errorf("%w: received %d bytes", video.ErrInvalidFileSize, req.FileSize)
	}

	if !strings.HasPrefix(strings.ToLower(req.MimeType), "video/") {
		return nil, ErrInvalidContentType
	}

	if req.UserID == "" {
		req.UserID = uuid.NewString()
	}

	// 2. De-duplication / Existing Active Session Check (Fingerprint)
	if req.FileFingerprint != "" {
		existingSession, err := s.sessionRepo.GetSessionByFingerprint(ctx, req.FileFingerprint)
		if err == nil && existingSession != nil && existingSession.CanUpload() {
			s.log.Info("Found existing active upload session for fingerprint %s (VideoID=%s)", req.FileFingerprint, existingSession.VideoID)
			return s.resumeExistingSession(ctx, existingSession)
		}
	}

	// 3. Adaptive Chunk Sizing (RFC-001)
	partSize := video.CalculateAdaptiveChunkSize(req.FileSize)
	totalParts := video.CalculateTotalParts(req.FileSize, partSize)

	videoID := uuid.NewString()
	storageKey := fmt.Sprintf("raw/%s/source.mp4", videoID)

	// 4. Create Multipart Upload on Cloudflare R2
	uploadID, err := s.storageProvider.CreateMultipartUpload(ctx, s.rawBucket, storageKey, req.MimeType)
	if err != nil {
		return nil, fmt.Errorf("storage provider failed to create multipart upload: %w", err)
	}

	expiresAt := time.Now().Add(s.sessionTTL)

	// 5. Persist Video & UploadSession in Database
	v := &video.Video{
		ID:            videoID,
		UserID:        req.UserID,
		Title:         req.Title,
		Description:   req.Description,
		RawBucket:     s.rawBucket,
		RawObjectKey:  storageKey,
		RawSizeBytes:  req.FileSize,
		MimeType:      req.MimeType,
		Status:        video.StatusPendingUpload,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := s.videoRepo.Save(ctx, v); err != nil {
		_ = s.storageProvider.AbortMultipartUpload(ctx, s.rawBucket, storageKey, uploadID)
		return nil, fmt.Errorf("failed to save video entity: %w", err)
	}

	session := &video.UploadSession{
		ID:               uuid.NewString(),
		VideoID:          videoID,
		UploadID:         uploadID,
		StorageBucket:    s.rawBucket,
		StorageKey:       storageKey,
		FileFingerprint:  req.FileFingerprint,
		TotalSizeBytes:   req.FileSize,
		PartSizeBytes:    partSize,
		TotalParts:       totalParts,
		Status:           video.SessionStatusUploading,
		ExpiresAt:        expiresAt,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if err := s.sessionRepo.SaveSession(ctx, session); err != nil {
		_ = s.storageProvider.AbortMultipartUpload(ctx, s.rawBucket, storageKey, uploadID)
		return nil, fmt.Errorf("failed to save upload session entity: %w", err)
	}

	// 6. Generate Presigned PUT URLs for All Initial Parts
	presignedParts := make([]video.PartPresignedURL, 0, totalParts)
	for i := 1; i <= totalParts; i++ {
		url, err := s.storageProvider.GeneratePresignedPartURL(ctx, s.rawBucket, storageKey, uploadID, i, s.sessionTTL)
		if err != nil {
			return nil, fmt.Errorf("failed to generate presigned url for part %d: %w", i, err)
		}
		presignedParts = append(presignedParts, video.PartPresignedURL{
			PartNumber: i,
			UploadURL:  url,
		})
	}

	s.log.Info("Successfully created multipart upload session: VideoID=%s, Parts=%d, PartSize=%d", videoID, totalParts, partSize)

	return &CreateSessionResponse{
		VideoID:       videoID,
		UploadID:      uploadID,
		PartSizeBytes: partSize,
		TotalParts:    totalParts,
		ExpiresAt:     expiresAt,
		Parts:         presignedParts,
		IsResumed:     false,
	}, nil
}

// GetUploadStatus returns progress and missing chunk URLs for resuming
func (s *UploadService) GetUploadStatus(ctx context.Context, videoID string) (*UploadStatusResponse, error) {
	session, err := s.sessionRepo.GetSessionByVideoID(ctx, videoID)
	if err != nil {
		return nil, ErrSessionNotFound
	}

	// Fetch acknowledged parts from storage or DB
	completedParts, err := s.storageProvider.ListUploadedParts(ctx, session.StorageBucket, session.StorageKey, session.UploadID)
	if err != nil {
		// Fallback to local DB records
		completedParts, _ = s.sessionRepo.GetCompletedParts(ctx, session.ID)
	}

	completedMap := make(map[int]string)
	for _, p := range completedParts {
		completedMap[p.PartNumber] = p.ETag
	}

	// Compute missing parts and generate fresh presigned URLs
	missingParts := make([]video.PartPresignedURL, 0)
	for i := 1; i <= session.TotalParts; i++ {
		if _, ok := completedMap[i]; !ok {
			url, err := s.storageProvider.GeneratePresignedPartURL(ctx, session.StorageBucket, session.StorageKey, session.UploadID, i, s.sessionTTL)
			if err != nil {
				return nil, fmt.Errorf("failed to refresh presigned url for part %d: %w", i, err)
			}
			missingParts = append(missingParts, video.PartPresignedURL{
				PartNumber: i,
				UploadURL:  url,
			})
		}
	}

	return &UploadStatusResponse{
		VideoID:             videoID,
		Status:              string(session.Status),
		TotalParts:          session.TotalParts,
		CompletedPartsCount: len(completedParts),
		CompletedParts:      completedParts,
		MissingParts:        missingParts,
	}, nil
}

// CompleteUpload handles final assembly and transitions video to UPLOADED
func (s *UploadService) CompleteUpload(ctx context.Context, videoID string, parts []video.CompletedPart) (*CompleteUploadResponse, error) {
	session, err := s.sessionRepo.GetSessionByVideoID(ctx, videoID)
	if err != nil {
		return nil, ErrSessionNotFound
	}

	v, err := s.videoRepo.FindByID(ctx, videoID)
	if err != nil {
		return nil, ErrVideoNotFound
	}

	// Idempotent completion check
	if v.Status == video.StatusUploaded || v.Status == video.StatusProcessing || v.Status == video.StatusReady {
		s.log.Info("Video %s is already completed (Status=%s). Returning idempotent OK.", videoID, v.Status)
		return &CompleteUploadResponse{
			VideoID: videoID,
			Status:  string(v.Status),
			Message: "Video upload already completed.",
		}, nil
	}

	// Sort parts ascending by PartNumber
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].PartNumber < parts[j].PartNumber
	})

	// Complete on Cloudflare R2
	_, completeErr := s.storageProvider.CompleteMultipartUpload(ctx, session.StorageBucket, session.StorageKey, session.UploadID, parts)
	if completeErr != nil {
		// RFC-001 Ghost Complete Check: check if the object exists anyway
		exists, checkErr := s.storageProvider.CheckObjectExists(ctx, session.StorageBucket, session.StorageKey)
		if checkErr != nil || !exists {
			s.log.Error("Failed to complete multipart upload on R2 for video %s: %v", videoID, completeErr)
			return nil, fmt.Errorf("failed to complete multipart upload: %w", completeErr)
		}
		s.log.Warn("Multipart session already finalized on R2 (Ghost Complete recovery triggered) for video %s", videoID)
	}

	// Verify object size from R2
	actualSize, err := s.storageProvider.GetObjectSize(ctx, session.StorageBucket, session.StorageKey)
	if err == nil && actualSize > 0 {
		v.RawSizeBytes = actualSize
	}

	// Record individual completed parts in DB
	for _, p := range parts {
		_ = s.sessionRepo.RecordPartUploaded(ctx, session.ID, p.PartNumber, p.ETag, session.PartSizeBytes)
	}

	// Update session status to COMPLETED
	_ = s.sessionRepo.UpdateSessionStatus(ctx, session.ID, video.SessionStatusCompleted)

	// Atomic update: PENDING_UPLOAD -> UPLOADED
	updated, err := s.videoRepo.UpdateStatusIf(ctx, videoID, video.StatusUploaded, video.StatusPendingUpload)
	if err != nil {
		return nil, fmt.Errorf("failed to update video status: %w", err)
	}
	if !updated {
		s.log.Warn("Conditional update affected 0 rows for video %s, checking current state", videoID)
	}

	s.log.Info("Successfully completed multipart upload for video %s (Size=%d bytes)", videoID, v.RawSizeBytes)

	// Milestone 4: Emit transcode job to RabbitMQ queue
	if s.jobQueue != nil && s.jobRepo != nil {
		targetBucket := s.processedBucket
		if targetBucket == "" {
			targetBucket = "bitmill-processed-videos"
		}
		targetPrefix := fmt.Sprintf("hls/%s", videoID)

		jobRecord, err := s.jobRepo.CreateJob(ctx, videoID, "", 3)
		if err != nil {
			s.log.Error("Failed to record processing job in DB for video %s: %v", videoID, err)
		} else {
			jobMsg := &video.TranscodeJobMessage{
				EventID:      uuid.New().String(),
				EventType:    "video.uploaded",
				Timestamp:    time.Now(),
				VideoID:      videoID,
				JobID:        jobRecord.ID,
				SourceBucket: session.StorageBucket,
				SourceKey:    session.StorageKey,
				TargetBucket: targetBucket,
				TargetPrefix: targetPrefix,
				AttemptCount: 1,
				MaxAttempts:  3,
			}
			if pubErr := s.jobQueue.PublishTranscodeJob(ctx, jobMsg); pubErr != nil {
				s.log.Error("Failed to publish transcode job to RabbitMQ for video %s: %v", videoID, pubErr)
			} else {
				s.log.Info("Dispatched transcode job to RabbitMQ for video %s (JobID=%s)", videoID, jobRecord.ID)
			}
		}
	}

	return &CompleteUploadResponse{
		VideoID: videoID,
		Status:  string(video.StatusUploaded),
		Message: "Upload completed successfully. Ready for transcoding.",
	}, nil
}

// AbortUpload cancels the multipart upload and purges staged parts from Cloudflare R2
func (s *UploadService) AbortUpload(ctx context.Context, videoID string) error {
	session, err := s.sessionRepo.GetSessionByVideoID(ctx, videoID)
	if err != nil {
		return ErrSessionNotFound
	}

	if err := s.storageProvider.AbortMultipartUpload(ctx, session.StorageBucket, session.StorageKey, session.UploadID); err != nil {
		s.log.Error("Failed to abort multipart upload on R2: %v", err)
	}

	_ = s.sessionRepo.UpdateSessionStatus(ctx, session.ID, video.SessionStatusAborted)
	_ = s.videoRepo.UpdateStatus(ctx, videoID, video.StatusFailed)

	s.log.Info("Aborted upload session for video %s and reclaimed storage", videoID)
	return nil
}

func (s *UploadService) resumeExistingSession(ctx context.Context, session *video.UploadSession) (*CreateSessionResponse, error) {
	statusResp, err := s.GetUploadStatus(ctx, session.VideoID)
	if err != nil {
		return nil, err
	}

	return &CreateSessionResponse{
		VideoID:       session.VideoID,
		UploadID:      session.UploadID,
		PartSizeBytes: session.PartSizeBytes,
		TotalParts:    session.TotalParts,
		ExpiresAt:     session.ExpiresAt,
		Parts:         statusResp.MissingParts,
		IsResumed:     true,
	}, nil
}
