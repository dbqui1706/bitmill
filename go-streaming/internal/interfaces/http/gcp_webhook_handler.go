package http

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"quidb.dev/go-streaming/internal/domain/video"
	"quidb.dev/go-streaming/pkg/logger"
)

// PubSubPushPayload represents the JSON body pushed by Google Cloud Pub/Sub
type PubSubPushPayload struct {
	Message struct {
		Data        string            `json:"data"`
		MessageID   string            `json:"messageId"`
		PublishTime string            `json:"publishTime"`
		Attributes  map[string]string `json:"attributes"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

// TranscoderJobNotification represents the decoded message published by Cloud Transcoder API
type TranscoderJobNotification struct {
	Job struct {
		Name      string            `json:"name"`
		State     string            `json:"state"` // "SUCCEEDED", "FAILED"
		Labels    map[string]string `json:"labels"`
		OutputUri string            `json:"outputUri"`
		Error     *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"job"`
}

// GCPWebhookHandler handles incoming Google Cloud Pub/Sub push notifications for Transcoder events
type GCPWebhookHandler struct {
	videoRepo     video.Repository
	jobRepo       video.JobRepository
	renditionRepo video.RenditionRepository
	publicDomain  string
	log           *logger.Logger
}

// NewGCPWebhookHandler creates a new handler instance
func NewGCPWebhookHandler(
	videoRepo video.Repository,
	jobRepo video.JobRepository,
	renditionRepo video.RenditionRepository,
	publicDomain string,
	log *logger.Logger,
) *GCPWebhookHandler {
	return &GCPWebhookHandler{
		videoRepo:     videoRepo,
		jobRepo:       jobRepo,
		renditionRepo: renditionRepo,
		publicDomain:  publicDomain,
		log:           log,
	}
}

// RegisterRoutes mounts the webhook endpoint on the Gin router
func (h *GCPWebhookHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/webhooks/gcp-transcoder", h.HandleTranscoderEvent)
}

// HandleTranscoderEvent receives and processes the Pub/Sub push notification
func (h *GCPWebhookHandler) HandleTranscoderEvent(c *gin.Context) {
	var payload PubSubPushPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.log.Warn("Invalid Pub/Sub push payload received: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload format"})
		return
	}

	rawBytes, err := base64.StdEncoding.DecodeString(payload.Message.Data)
	if err != nil {
		h.log.Warn("Failed to decode base64 Pub/Sub data: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid base64 encoding"})
		return
	}

	var notification TranscoderJobNotification
	if err := json.Unmarshal(rawBytes, &notification); err != nil {
		h.log.Warn("Failed to unmarshal Transcoder job notification: %v", err)
		c.JSON(http.StatusOK, gin.H{"status": "ignored_unparsable_payload"})
		return
	}

	videoID := notification.Job.Labels["video_id"]
	h.log.Info("Received GCP Transcoder event: Job=%s, State=%s, VideoID=%s",
		notification.Job.Name, notification.Job.State, videoID)

	if videoID == "" {
		h.log.Warn("GCP Transcoder notification missing video_id label: %s", string(rawBytes))
		c.JSON(http.StatusOK, gin.H{"status": "ignored_missing_video_id"})
		return
	}

	ctx := c.Request.Context()

	if notification.Job.State == "SUCCEEDED" {
		// 1. Update Video Status to READY
		if err := h.videoRepo.UpdateStatus(ctx, videoID, video.StatusReady); err != nil {
			h.log.Error("Failed to update video status to READY for %s: %v", videoID, err)
		}

		// 2. Register 4-Rung ABR Renditions
		renditions := []video.Rendition{
			{
				ResolutionName:   "1080p",
				Width:            1920,
				Height:           1080,
				VideoBitrateKbps: 4500,
				AudioBitrateKbps: 128,
				PlaylistPath:     fmt.Sprintf("videos/%s/hls/1080p.m3u8", videoID),
				Status:           "READY",
			},
			{
				ResolutionName:   "720p",
				Width:            1280,
				Height:           720,
				VideoBitrateKbps: 2500,
				AudioBitrateKbps: 128,
				PlaylistPath:     fmt.Sprintf("videos/%s/hls/720p.m3u8", videoID),
				Status:           "READY",
			},
			{
				ResolutionName:   "480p",
				Width:            854,
				Height:           480,
				VideoBitrateKbps: 1000,
				AudioBitrateKbps: 128,
				PlaylistPath:     fmt.Sprintf("videos/%s/hls/480p.m3u8", videoID),
				Status:           "READY",
			},
			{
				ResolutionName:   "360p",
				Width:            640,
				Height:           360,
				VideoBitrateKbps: 600,
				AudioBitrateKbps: 128,
				PlaylistPath:     fmt.Sprintf("videos/%s/hls/360p.m3u8", videoID),
				Status:           "READY",
			},
		}

		if h.renditionRepo != nil {
			if err := h.renditionRepo.SaveRenditions(ctx, videoID, renditions); err != nil {
				h.log.Error("Failed to persist ABR renditions for %s: %v", videoID, err)
			}
		}

		h.log.Info("Successfully finalized Video %s via GCP Transcoder notification", videoID)
		c.JSON(http.StatusOK, gin.H{"status": "success", "video_id": videoID})
		return
	}

	if notification.Job.State == "FAILED" {
		errMsg := "transcoding failed"
		if notification.Job.Error != nil {
			errMsg = fmt.Sprintf("code %d: %s", notification.Job.Error.Code, notification.Job.Error.Message)
		}
		h.log.Error("GCP Transcoder job failed for VideoID %s: %s", videoID, errMsg)
		_ = h.videoRepo.MarkFailed(ctx, videoID)
		c.JSON(http.StatusOK, gin.H{"status": "marked_failed", "video_id": videoID})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "acknowledged", "state": notification.Job.State})
}
