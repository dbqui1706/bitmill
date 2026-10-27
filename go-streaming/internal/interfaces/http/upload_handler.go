package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	videoApp "quidb.dev/go-streaming/internal/application/video"
	"quidb.dev/go-streaming/internal/domain/video"
	"quidb.dev/go-streaming/pkg/logger"
	"quidb.dev/go-streaming/pkg/response"
)

type UploadHandler struct {
	uploadService *videoApp.UploadService
	log           *logger.Logger
}

func NewUploadHandler(uploadService *videoApp.UploadService, log *logger.Logger) *UploadHandler {
	return &UploadHandler{
		uploadService: uploadService,
		log:           log,
	}
}

func (h *UploadHandler) RegisterRoutes(r *gin.Engine) {
	v1 := r.Group("/api/v1/videos")
	{
		v1.POST("/upload-session", h.CreateUploadSession)
		v1.GET("/:id/upload-status", h.GetUploadStatus)
		v1.POST("/:id/complete-upload", h.CompleteUpload)
		v1.DELETE("/:id/upload-session", h.AbortUpload)
	}
}

type createUploadSessionReq struct {
	UserID          string `json:"user_id"`
	Title           string `json:"title" binding:"required"`
	Description     string `json:"description"`
	FileName        string `json:"file_name" binding:"required"`
	FileSize        int64  `json:"file_size" binding:"required,gt=0"`
	MimeType        string `json:"mime_type" binding:"required"`
	FileFingerprint string `json:"file_fingerprint"`
}

func (h *UploadHandler) CreateUploadSession(c *gin.Context) {
	var req createUploadSessionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	appReq := videoApp.CreateSessionRequest{
		UserID:          req.UserID,
		Title:           req.Title,
		Description:     req.Description,
		FileName:        req.FileName,
		FileSize:        req.FileSize,
		MimeType:        req.MimeType,
		FileFingerprint: req.FileFingerprint,
	}

	res, err := h.uploadService.CreateUploadSession(c.Request.Context(), appReq)
	if err != nil {
		if errors.Is(err, video.ErrInvalidFileSize) || errors.Is(err, videoApp.ErrInvalidContentType) {
			response.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		h.log.Error("CreateUploadSession error: %v", err)
		response.Error(c, http.StatusInternalServerError, "Failed to create upload session: "+err.Error())
		return
	}

	statusCode := http.StatusCreated
	if res.IsResumed {
		statusCode = http.StatusOK
	}
	response.Success(c, statusCode, res)
}

func (h *UploadHandler) GetUploadStatus(c *gin.Context) {
	videoID := c.Param("id")
	if videoID == "" {
		response.Error(c, http.StatusBadRequest, "Missing video id parameter")
		return
	}

	res, err := h.uploadService.GetUploadStatus(c.Request.Context(), videoID)
	if err != nil {
		if errors.Is(err, videoApp.ErrSessionNotFound) {
			response.Error(c, http.StatusNotFound, "Upload session not found")
			return
		}
		h.log.Error("GetUploadStatus error: %v", err)
		response.Error(c, http.StatusInternalServerError, "Failed to query upload status: "+err.Error())
		return
	}

	response.Success(c, http.StatusOK, res)
}

type completeUploadReq struct {
	Parts []video.CompletedPart `json:"parts" binding:"required,min=1"`
}

func (h *UploadHandler) CompleteUpload(c *gin.Context) {
	videoID := c.Param("id")
	if videoID == "" {
		response.Error(c, http.StatusBadRequest, "Missing video id parameter")
		return
	}

	var req completeUploadReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "Invalid request payload: "+err.Error())
		return
	}

	res, err := h.uploadService.CompleteUpload(c.Request.Context(), videoID, req.Parts)
	if err != nil {
		if errors.Is(err, videoApp.ErrSessionNotFound) || errors.Is(err, videoApp.ErrVideoNotFound) {
			response.Error(c, http.StatusNotFound, err.Error())
			return
		}
		h.log.Error("CompleteUpload error: %v", err)
		response.Error(c, http.StatusInternalServerError, "Failed to complete upload: "+err.Error())
		return
	}

	response.Success(c, http.StatusOK, res)
}

func (h *UploadHandler) AbortUpload(c *gin.Context) {
	videoID := c.Param("id")
	if videoID == "" {
		response.Error(c, http.StatusBadRequest, "Missing video id parameter")
		return
	}

	if err := h.uploadService.AbortUpload(c.Request.Context(), videoID); err != nil {
		if errors.Is(err, videoApp.ErrSessionNotFound) {
			response.Error(c, http.StatusNotFound, "Upload session not found")
			return
		}
		h.log.Error("AbortUpload error: %v", err)
		response.Error(c, http.StatusInternalServerError, "Failed to abort upload: "+err.Error())
		return
	}

	response.Success(c, http.StatusOK, gin.H{
		"video_id": videoID,
		"status":   "ABORTED",
		"message":  "Upload session aborted and storage reclaimed",
	})
}
