package http

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"quidb.dev/go-streaming/internal/domain/video"
	"quidb.dev/go-streaming/pkg/logger"
)

type mockVideoRepo struct {
	mock.Mock
}

func (m *mockVideoRepo) Save(ctx context.Context, v *video.Video) error {
	args := m.Called(ctx, v)
	return args.Error(0)
}

func (m *mockVideoRepo) FindByID(ctx context.Context, id string) (*video.Video, error) {
	args := m.Called(ctx, id)
	return args.Get(0).(*video.Video), args.Error(1)
}

func (m *mockVideoRepo) List(ctx context.Context) ([]*video.Video, error) {
	args := m.Called(ctx)
	return args.Get(0).([]*video.Video), args.Error(1)
}

func (m *mockVideoRepo) UpdateStatus(ctx context.Context, id string, status video.Status) error {
	args := m.Called(ctx, id, status)
	return args.Error(0)
}

func (m *mockVideoRepo) UpdateStatusIf(ctx context.Context, id string, newStatus, expectedCurrentStatus video.Status) (bool, error) {
	args := m.Called(ctx, id, newStatus, expectedCurrentStatus)
	return args.Bool(0), args.Error(1)
}

func (m *mockVideoRepo) MarkReady(ctx context.Context, id, masterPlaylistURL, thumbnailURL string, durationSeconds float64) error {
	args := m.Called(ctx, id, masterPlaylistURL, thumbnailURL, durationSeconds)
	return args.Error(0)
}

func (m *mockVideoRepo) MarkFailed(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

type mockRenditionRepo struct {
	mock.Mock
}

func (m *mockRenditionRepo) SaveRenditions(ctx context.Context, videoID string, renditions []video.Rendition) error {
	args := m.Called(ctx, videoID, renditions)
	return args.Error(0)
}

func (m *mockRenditionRepo) ListRenditions(ctx context.Context, videoID string) ([]video.Rendition, error) {
	args := m.Called(ctx, videoID)
	return args.Get(0).([]video.Rendition), args.Error(1)
}

func TestGCPWebhookHandler_HandleTranscoderEvent_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	log := logger.New()

	vRepo := new(mockVideoRepo)
	rRepo := new(mockRenditionRepo)

	vRepo.On("UpdateStatus", mock.Anything, "test-vid-123", video.StatusReady).Return(nil)
	rRepo.On("SaveRenditions", mock.Anything, "test-vid-123", mock.MatchedBy(func(r []video.Rendition) bool {
		return len(r) == 4 && r[0].ResolutionName == "1080p" && r[3].ResolutionName == "360p"
	})).Return(nil)

	handler := NewGCPWebhookHandler(vRepo, nil, rRepo, "https://storage.googleapis.com/bitmill-processed-videos", log)

	router := gin.New()
	handler.RegisterRoutes(router.Group("/api/v1"))

	notification := TranscoderJobNotification{}
	notification.Job.Name = "projects/123/locations/asia-southeast1/jobs/job-xyz"
	notification.Job.State = "SUCCEEDED"
	notification.Job.Labels = map[string]string{"video_id": "test-vid-123"}
	notificationJSON, _ := json.Marshal(notification)

	payload := PubSubPushPayload{}
	payload.Message.Data = base64.StdEncoding.EncodeToString(notificationJSON)
	payload.Message.MessageID = "msg-123"
	payloadJSON, _ := json.Marshal(payload)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/webhooks/gcp-transcoder", bytes.NewReader(payloadJSON))
	req.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	vRepo.AssertExpectations(t)
	rRepo.AssertExpectations(t)
}

func TestGCPWebhookHandler_HandleTranscoderEvent_Failure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	log := logger.New()

	vRepo := new(mockVideoRepo)
	vRepo.On("MarkFailed", mock.Anything, "test-vid-fail").Return(nil)

	handler := NewGCPWebhookHandler(vRepo, nil, nil, "", log)

	router := gin.New()
	handler.RegisterRoutes(router.Group("/api/v1"))

	notification := TranscoderJobNotification{}
	notification.Job.Name = "projects/123/locations/asia-southeast1/jobs/job-fail"
	notification.Job.State = "FAILED"
	notification.Job.Labels = map[string]string{"video_id": "test-vid-fail"}
	notificationJSON, _ := json.Marshal(notification)

	payload := PubSubPushPayload{}
	payload.Message.Data = base64.StdEncoding.EncodeToString(notificationJSON)
	payloadJSON, _ := json.Marshal(payload)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/webhooks/gcp-transcoder", bytes.NewReader(payloadJSON))
	req.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	vRepo.AssertExpectations(t)
}
