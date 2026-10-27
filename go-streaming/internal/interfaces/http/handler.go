package http

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
	videoApp "quidb.dev/go-streaming/internal/application/video"
	"quidb.dev/go-streaming/pkg/logger"
	"quidb.dev/go-streaming/pkg/response"
)

//go:embed web/index.html
var dashboardHTML string

type Handler struct {
	service    *videoApp.Service
	hlsRootDir string
	log        *logger.Logger
}

func NewHandler(service *videoApp.Service, hlsRootDir string, log *logger.Logger) *Handler {
	return &Handler{
		service:    service,
		hlsRootDir: hlsRootDir,
		log:        log,
	}
}

// RegisterRoutes đăng ký toàn bộ routing vào Gin Engine
func (h *Handler) RegisterRoutes(r *gin.Engine) {
	// 1. Phục vụ Static HLS (.m3u8 và .ts) cho trình duyệt
	r.Static("/hls", h.hlsRootDir)

	// 2. Nhóm REST APIs
	api := r.Group("/api")
	{
		api.GET("/videos", h.ListVideos)
		api.POST("/videos", h.RegisterVideo)
		api.GET("/videos/:id", h.GetVideo)
		api.POST("/videos/:id/process", h.ProcessVideo)
	}

	// 3. Web Player Dashboard giao diện
	r.GET("/", h.Dashboard)
}

type registerVideoRequest struct {
	ID         string `json:"id" binding:"required"`
	Title      string `json:"title" binding:"required"`
	SourcePath string `json:"source_path" binding:"required"`
}

func (h *Handler) ListVideos(c *gin.Context) {
	videos, err := h.service.ListVideos(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, http.StatusOK, videos)
}

func (h *Handler) RegisterVideo(c *gin.Context) {
	var req registerVideoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "Dữ liệu JSON không hợp lệ: "+err.Error())
		return
	}

	v, err := h.service.RegisterVideo(c.Request.Context(), req.ID, req.Title, req.SourcePath)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, http.StatusCreated, v)
}

func (h *Handler) GetVideo(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		response.Error(c, http.StatusBadRequest, "Thiếu video ID")
		return
	}

	v, err := h.service.GetVideo(c.Request.Context(), id)
	if err != nil {
		response.Error(c, http.StatusNotFound, "Không tìm thấy video")
		return
	}
	response.Success(c, http.StatusOK, v)
}

func (h *Handler) ProcessVideo(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		response.Error(c, http.StatusBadRequest, "Thiếu video ID")
		return
	}

	v, err := h.service.ProcessVideo(c.Request.Context(), id)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, http.StatusOK, v)
}

func (h *Handler) Dashboard(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, dashboardHTML)
}
