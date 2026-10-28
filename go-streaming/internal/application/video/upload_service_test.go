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

// ---------------------------------------------------------------------------
// Pure Mock Implementations (Proving 100% Inversion of Control & Testability)
// ---------------------------------------------------------------------------

type mockStorageProvider struct {
	createMultipartFunc   func(ctx context.Context, bucket, objectKey, contentType string) (string, error)
	presignPartFunc       func(ctx context.Context, bucket, objectKey, uploadID string, partNumber int, expiry time.Duration) (string, error)
	completeMultipartFunc func(ctx context.Context, bucket, objectKey, uploadID string, parts []video.CompletedPart) (string, error)
	abortMultipartFunc    func(ctx context.Context, bucket, objectKey, uploadID string) error
	listUploadedPartsFunc func(ctx context.Context, bucket, objectKey, uploadID string) ([]video.CompletedPart, error)
	checkObjectExistsFunc func(ctx context.Context, bucket, objectKey string) (bool, error)
	getObjectSizeFunc     func(ctx context.Context, bucket, objectKey string) (int64, error)
}

func (m *mockStorageProvider) GeneratePresignedPutURL(ctx context.Context, bucket, objectKey string, expiry time.Duration, contentType string) (string, error) {
	return "https://r2.mock/single-put", nil
}
func (m *mockStorageProvider) CreateMultipartUpload(ctx context.Context, bucket, objectKey, contentType string) (string, error) {
	if m.createMultipartFunc != nil {
		return m.createMultipartFunc(ctx, bucket, objectKey, contentType)
	}
	return "mock-upload-id-123", nil
}
func (m *mockStorageProvider) GeneratePresignedPartURL(ctx context.Context, bucket, objectKey, uploadID string, partNumber int, expiry time.Duration) (string, error) {
	if m.presignPartFunc != nil {
		return m.presignPartFunc(ctx, bucket, objectKey, uploadID, partNumber, expiry)
	}
	return "https://r2.mock/part-" + string(rune(partNumber)), nil
}
func (m *mockStorageProvider) CompleteMultipartUpload(ctx context.Context, bucket, objectKey, uploadID string, parts []video.CompletedPart) (string, error) {
	if m.completeMultipartFunc != nil {
		return m.completeMultipartFunc(ctx, bucket, objectKey, uploadID, parts)
	}
	return "https://r2.mock/completed", nil
}
func (m *mockStorageProvider) AbortMultipartUpload(ctx context.Context, bucket, objectKey, uploadID string) error {
	if m.abortMultipartFunc != nil {
		return m.abortMultipartFunc(ctx, bucket, objectKey, uploadID)
	}
	return nil
}
func (m *mockStorageProvider) ListUploadedParts(ctx context.Context, bucket, objectKey, uploadID string) ([]video.CompletedPart, error) {
	if m.listUploadedPartsFunc != nil {
		return m.listUploadedPartsFunc(ctx, bucket, objectKey, uploadID)
	}
	return nil, nil
}
func (m *mockStorageProvider) CheckObjectExists(ctx context.Context, bucket, objectKey string) (bool, error) {
	if m.checkObjectExistsFunc != nil {
		return m.checkObjectExistsFunc(ctx, bucket, objectKey)
	}
	return true, nil
}
func (m *mockStorageProvider) GetObjectSize(ctx context.Context, bucket, objectKey string) (int64, error) {
	if m.getObjectSizeFunc != nil {
		return m.getObjectSizeFunc(ctx, bucket, objectKey)
	}
	return 10485760, nil
}
func (m *mockStorageProvider) GetDownloadURL(ctx context.Context, bucket, objectKey string, expiry time.Duration) (string, error) {
	return "https://r2.mock/download", nil
}
func (m *mockStorageProvider) DownloadToFile(ctx context.Context, bucket, objectKey, localPath string) error {
	return nil
}
func (m *mockStorageProvider) UploadFile(ctx context.Context, localPath, bucket, objectKey, contentType, cacheControl string) error {
	return nil
}
func (m *mockStorageProvider) UploadDirectory(ctx context.Context, localDir, bucket, prefix string) error {
	return nil
}

type mockVideoRepo struct {
	videos map[string]*video.Video
}

func newMockVideoRepo() *mockVideoRepo {
	return &mockVideoRepo{videos: make(map[string]*video.Video)}
}
func (m *mockVideoRepo) Save(ctx context.Context, v *video.Video) error {
	m.videos[v.ID] = v
	return nil
}
func (m *mockVideoRepo) FindByID(ctx context.Context, id string) (*video.Video, error) {
	v, ok := m.videos[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return v, nil
}
func (m *mockVideoRepo) List(ctx context.Context) ([]*video.Video, error) {
	res := make([]*video.Video, 0, len(m.videos))
	for _, v := range m.videos {
		res = append(res, v)
	}
	return res, nil
}
func (m *mockVideoRepo) UpdateStatus(ctx context.Context, id string, status video.Status) error {
	v, ok := m.videos[id]
	if !ok {
		return errors.New("not found")
	}
	v.Status = status
	return nil
}
func (m *mockVideoRepo) UpdateStatusIf(ctx context.Context, id string, newStatus video.Status, currentStatus video.Status) (bool, error) {
	v, ok := m.videos[id]
	if !ok {
		return false, errors.New("not found")
	}
	if v.Status == currentStatus {
		v.Status = newStatus
		return true, nil
	}
	return false, nil
}
func (m *mockVideoRepo) MarkReady(ctx context.Context, id, masterPlaylistURL, thumbnailURL string, durationSeconds float64) error {
	v, ok := m.videos[id]
	if !ok {
		return errors.New("not found")
	}
	v.MarkReady(masterPlaylistURL, durationSeconds)
	v.ThumbnailURL = thumbnailURL
	return nil
}
func (m *mockVideoRepo) MarkFailed(ctx context.Context, id string) error {
	v, ok := m.videos[id]
	if !ok {
		return errors.New("not found")
	}
	v.MarkFailed()
	return nil
}

type mockSessionRepo struct {
	sessions map[string]*video.UploadSession
	parts    map[string][]video.CompletedPart
}

func newMockSessionRepo() *mockSessionRepo {
	return &mockSessionRepo{
		sessions: make(map[string]*video.UploadSession),
		parts:    make(map[string][]video.CompletedPart),
	}
}
func (m *mockSessionRepo) SaveSession(ctx context.Context, session *video.UploadSession) error {
	m.sessions[session.VideoID] = session
	return nil
}
func (m *mockSessionRepo) GetSessionByVideoID(ctx context.Context, videoID string) (*video.UploadSession, error) {
	s, ok := m.sessions[videoID]
	if !ok {
		return nil, errors.New("not found")
	}
	return s, nil
}
func (m *mockSessionRepo) GetSessionByFingerprint(ctx context.Context, fingerprint string) (*video.UploadSession, error) {
	for _, s := range m.sessions {
		if s.FileFingerprint == fingerprint && s.CanUpload() {
			return s, nil
		}
	}
	return nil, nil
}
func (m *mockSessionRepo) UpdateSessionStatus(ctx context.Context, sessionID string, status video.UploadSessionStatus) error {
	for _, s := range m.sessions {
		if s.ID == sessionID {
			s.Status = status
			return nil
		}
	}
	return nil
}
func (m *mockSessionRepo) RecordPartUploaded(ctx context.Context, sessionID string, partNumber int, etag string, sizeBytes int64) error {
	m.parts[sessionID] = append(m.parts[sessionID], video.CompletedPart{PartNumber: partNumber, ETag: etag})
	return nil
}
func (m *mockSessionRepo) GetCompletedParts(ctx context.Context, sessionID string) ([]video.CompletedPart, error) {
	return m.parts[sessionID], nil
}
func (m *mockSessionRepo) ListExpiredSessions(ctx context.Context, olderThan time.Duration) ([]*video.UploadSession, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Unit Test Cases
// ---------------------------------------------------------------------------

func TestUploadService_CreateSession_BoundaryValidation(t *testing.T) {
	vRepo := newMockVideoRepo()
	sRepo := newMockSessionRepo()
	storage := &mockStorageProvider{}
	svc := videoApp.NewUploadService(vRepo, sRepo, storage, "test-bucket", time.Hour, logger.New())

	ctx := context.Background()

	// 1. File size < 5MB must be rejected
	_, err := svc.CreateUploadSession(ctx, videoApp.CreateSessionRequest{
		Title:    "Too small",
		FileName: "tiny.mp4",
		FileSize: 1024, // 1KB
		MimeType: "video/mp4",
	})
	if err == nil {
		t.Fatalf("expected error for file < 5MB, got nil")
	}

	// 2. Non-video mime type must be rejected
	_, err = svc.CreateUploadSession(ctx, videoApp.CreateSessionRequest{
		Title:    "Malicious exe",
		FileName: "malicious.exe",
		FileSize: 10 * 1024 * 1024,
		MimeType: "application/x-msdownload",
	})
	if err == nil {
		t.Fatalf("expected error for non-video MIME, got nil")
	}

	// 3. Valid 50MB video -> Adaptive chunking (10MB for <= 250MB)
	resp, err := svc.CreateUploadSession(ctx, videoApp.CreateSessionRequest{
		Title:           "Valid 50MB Lecture",
		FileName:        "lecture.mp4",
		FileSize:        50 * 1024 * 1024,
		MimeType:        "video/mp4",
		FileFingerprint: "fp-50mb",
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if resp.PartSizeBytes != 10*1024*1024 {
		t.Errorf("expected 10MB part size, got %d", resp.PartSizeBytes)
	}
	if resp.TotalParts != 5 {
		t.Errorf("expected 5 parts for 50MB, got %d", resp.TotalParts)
	}
	if len(resp.Parts) != 5 {
		t.Errorf("expected 5 presigned URLs, got %d", len(resp.Parts))
	}
}

func TestUploadService_AdaptiveChunking_LargeFile(t *testing.T) {
	vRepo := newMockVideoRepo()
	sRepo := newMockSessionRepo()
	storage := &mockStorageProvider{}
	svc := videoApp.NewUploadService(vRepo, sRepo, storage, "test-bucket", time.Hour, logger.New())

	ctx := context.Background()

	// 1GB file (> 250MB) -> Adaptive chunking selects 20MB
	resp, err := svc.CreateUploadSession(ctx, videoApp.CreateSessionRequest{
		Title:    "1GB Movie",
		FileName: "movie.mp4",
		FileSize: 1024 * 1024 * 1024, // 1GB
		MimeType: "video/mp4",
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	if resp.PartSizeBytes != 20*1024*1024 {
		t.Errorf("expected 20MB chunks for 1GB file, got %d", resp.PartSizeBytes)
	}
	if resp.TotalParts != 52 { // ceil(1024 / 20) = 52
		t.Errorf("expected 52 parts, got %d", resp.TotalParts)
	}
}

func TestUploadService_CompleteUpload_Idempotency(t *testing.T) {
	vRepo := newMockVideoRepo()
	sRepo := newMockSessionRepo()
	storage := &mockStorageProvider{}
	svc := videoApp.NewUploadService(vRepo, sRepo, storage, "test-bucket", time.Hour, logger.New())

	ctx := context.Background()

	sessionResp, err := svc.CreateUploadSession(ctx, videoApp.CreateSessionRequest{
		Title:    "Test Video",
		FileName: "test.mp4",
		FileSize: 20 * 1024 * 1024,
		MimeType: "video/mp4",
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	parts := []video.CompletedPart{
		{PartNumber: 1, ETag: "\"etag-1\""},
		{PartNumber: 2, ETag: "\"etag-2\""},
	}

	// First complete call
	res1, err := svc.CompleteUpload(ctx, sessionResp.VideoID, parts)
	if err != nil {
		t.Fatalf("first complete failed: %v", err)
	}
	if res1.Status != string(video.StatusUploaded) {
		t.Errorf("expected UPLOADED, got %s", res1.Status)
	}

	// Second complete call (Idempotent test)
	res2, err := svc.CompleteUpload(ctx, sessionResp.VideoID, parts)
	if err != nil {
		t.Fatalf("second complete (idempotent) failed: %v", err)
	}
	if res2.Status != string(video.StatusUploaded) {
		t.Errorf("expected UPLOADED on idempotent call, got %s", res2.Status)
	}
}

func TestUploadService_AbortUpload(t *testing.T) {
	vRepo := newMockVideoRepo()
	sRepo := newMockSessionRepo()
	abortCalled := false
	storage := &mockStorageProvider{
		abortMultipartFunc: func(ctx context.Context, bucket, objectKey, uploadID string) error {
			abortCalled = true
			return nil
		},
	}
	svc := videoApp.NewUploadService(vRepo, sRepo, storage, "test-bucket", time.Hour, logger.New())

	ctx := context.Background()

	sessionResp, err := svc.CreateUploadSession(ctx, videoApp.CreateSessionRequest{
		Title:    "Abort Me",
		FileName: "abort.mp4",
		FileSize: 10 * 1024 * 1024,
		MimeType: "video/mp4",
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	if err := svc.AbortUpload(ctx, sessionResp.VideoID); err != nil {
		t.Fatalf("abort failed: %v", err)
	}

	if !abortCalled {
		t.Errorf("expected storage.AbortMultipartUpload to be called")
	}

	v, _ := vRepo.FindByID(ctx, sessionResp.VideoID)
	if v.Status != video.StatusFailed {
		t.Errorf("expected video status FAILED, got %s", v.Status)
	}
}
