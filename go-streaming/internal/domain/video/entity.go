package video

import (
	"errors"
	"time"
)

// Status represents the lifecycle state of a video in the pipeline
type Status string

const (
	StatusPendingUpload Status = "PENDING_UPLOAD" // Presigned URL issued; awaiting client upload
	StatusUploaded      Status = "UPLOADED"       // Raw media received in R2; job queued
	StatusProcessing    Status = "PROCESSING"     // Transcoder worker actively slicing HLS
	StatusReady         Status = "READY"          // All HLS renditions generated & validated
	StatusFailed        Status = "FAILED"         // Processing encountered fatal unrecoverable error

	// Legacy alias for backwards compatibility
	StatusPending Status = StatusPendingUpload
)

var (
	ErrVideoNotFound = errors.New("video not found")
	ErrInvalidInput  = errors.New("invalid video input")
)

// Rendition represents a specific resolution and bitrate variant for ABR HLS streaming
type Rendition struct {
	ResolutionName   string `json:"resolution_name"` // "1080p", "720p", "480p", "360p"
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	VideoBitrateKbps int    `json:"video_bitrate_kbps"`
	AudioBitrateKbps int    `json:"audio_bitrate_kbps"`
	PlaylistPath     string `json:"playlist_path"`
	SegmentCount     int    `json:"segment_count"`
	Status           string `json:"status"` // "READY", "FAILED"
}

// Video is the central Domain Entity representing a VOD media asset
type Video struct {
	ID                string      `json:"id"`
	UserID            string      `json:"user_id,omitempty"`
	Title             string      `json:"title"`
	Description       string      `json:"description,omitempty"`
	SourcePath        string      `json:"source_path,omitempty"` // Local path (for local dev tests)
	RawBucket         string      `json:"raw_bucket,omitempty"`
	RawObjectKey      string      `json:"raw_object_key,omitempty"`
	RawSizeBytes      int64       `json:"raw_size_bytes,omitempty"`
	MimeType          string      `json:"mime_type,omitempty"`
	DurationSeconds   float64     `json:"duration_seconds,omitempty"`
	HLSURL            string      `json:"hls_url,omitempty"`             // Primary stream URL
	MasterPlaylistURL string      `json:"master_playlist_url,omitempty"` // HLS master.m3u8 CDN URL
	ThumbnailURL      string      `json:"thumbnail_url,omitempty"`
	Status            Status      `json:"status"`
	Renditions        []Rendition `json:"renditions,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
}

// NewVideo creates a new Video entity in PENDING_UPLOAD status
func NewVideo(id, title, sourcePath string) (*Video, error) {
	if id == "" || title == "" {
		return nil, ErrInvalidInput
	}
	now := time.Now()
	return &Video{
		ID:         id,
		Title:      title,
		SourcePath: sourcePath,
		Status:     StatusPendingUpload,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// NewR2Video creates a new Video entity configured for Cloudflare R2 ingestion
func NewR2Video(id, title, bucket, objectKey, mimeType string, expectedSize int64) (*Video, error) {
	if id == "" || title == "" || bucket == "" || objectKey == "" {
		return nil, ErrInvalidInput
	}
	now := time.Now()
	return &Video{
		ID:           id,
		Title:        title,
		RawBucket:    bucket,
		RawObjectKey: objectKey,
		MimeType:     mimeType,
		RawSizeBytes: expectedSize,
		Status:       StatusPendingUpload,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// State transition methods enforcing domain invariants
func (v *Video) MarkUploaded(sizeBytes int64) {
	v.Status = StatusUploaded
	if sizeBytes > 0 {
		v.RawSizeBytes = sizeBytes
	}
	v.UpdatedAt = time.Now()
}

func (v *Video) MarkProcessing() {
	v.Status = StatusProcessing
	v.UpdatedAt = time.Now()
}

func (v *Video) MarkReady(masterURL string, duration ...float64) {
	v.Status = StatusReady
	v.HLSURL = masterURL
	v.MasterPlaylistURL = masterURL
	if len(duration) > 0 {
		v.DurationSeconds = duration[0]
	}
	v.UpdatedAt = time.Now()
}

func (v *Video) SetRenditions(renditions []Rendition) {
	v.Renditions = renditions
}

func (v *Video) MarkFailed() {
	v.Status = StatusFailed
	v.UpdatedAt = time.Now()
}
