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

// JobStatus represents the lifecycle state of a background transcode task
type JobStatus string

const (
	JobStatusQueued     JobStatus = "QUEUED"
	JobStatusProcessing JobStatus = "PROCESSING"
	JobStatusCompleted  JobStatus = "COMPLETED"
	JobStatusFailed     JobStatus = "FAILED"
	JobStatusDLQ        JobStatus = "DLQ"
)

// ProcessingJob tracks the worker execution lifecycle for a video asset
type ProcessingJob struct {
	ID           string     `json:"id"`
	VideoID      string     `json:"video_id"`
	WorkerID     string     `json:"worker_id,omitempty"`
	Status       JobStatus  `json:"status"`
	AttemptCount int        `json:"attempt_count"`
	MaxAttempts  int        `json:"max_attempts"`
	ErrorMessage string     `json:"error_message,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// TranscodeJobMessage represents the immutable AMQP event payload published to RabbitMQ
type TranscodeJobMessage struct {
	EventID      string    `json:"event_id"`
	EventType    string    `json:"event_type"`
	Timestamp    time.Time `json:"timestamp"`
	VideoID      string    `json:"video_id"`
	JobID        string    `json:"job_id"`
	SourceBucket string    `json:"source_bucket"`
	SourceKey    string    `json:"source_key"`
	TargetBucket string    `json:"target_bucket"`
	TargetPrefix string    `json:"target_prefix"`
	AttemptCount int       `json:"attempt_count"`
	MaxAttempts  int       `json:"max_attempts"`
}

// MediaMetadata represents container probe telemetry from ffprobe
type MediaMetadata struct {
	DurationSeconds float64 `json:"duration_seconds"`
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	VideoCodec      string  `json:"video_codec"`
	AudioCodec      string  `json:"audio_codec"`
	BitrateKbps     int     `json:"bitrate_kbps"`
}

// RenditionProfile holds encoding targets for a specific resolution
type RenditionProfile struct {
	Name             string `json:"name"` // "4k", "1440p", "1080p", "720p", "480p", "360p"
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	VideoBitrateKbps int    `json:"video_bitrate_kbps"`
	AudioBitrateKbps int    `json:"audio_bitrate_kbps"`
}

// DefaultMasterLadder defines master ABR ladder presets from 4K down to 360p
var DefaultMasterLadder = []RenditionProfile{
	{Name: "4k", Width: 3840, Height: 2160, VideoBitrateKbps: 12000, AudioBitrateKbps: 192},
	{Name: "1440p", Width: 2560, Height: 1440, VideoBitrateKbps: 8000, AudioBitrateKbps: 192},
	{Name: "1080p", Width: 1920, Height: 1080, VideoBitrateKbps: 4500, AudioBitrateKbps: 128},
	{Name: "720p", Width: 1280, Height: 720, VideoBitrateKbps: 2500, AudioBitrateKbps: 128},
	{Name: "480p", Width: 854, Height: 480, VideoBitrateKbps: 1200, AudioBitrateKbps: 128},
	{Name: "360p", Width: 640, Height: 360, VideoBitrateKbps: 600, AudioBitrateKbps: 96},
}

// SelectRenditions dynamically filters master profiles where profile.Height <= sourceHeight
// Enforces the "Never Upscale" rule while preserving 4K/2K when uploaded.
func SelectRenditions(sourceHeight int) []RenditionProfile {
	var selected []RenditionProfile
	for _, p := range DefaultMasterLadder {
		if p.Height <= sourceHeight {
			selected = append(selected, p)
		}
	}
	// Fallback to baseline 360p if source is lower than 360p
	if len(selected) == 0 && len(DefaultMasterLadder) > 0 {
		selected = append(selected, DefaultMasterLadder[len(DefaultMasterLadder)-1])
	}
	return selected
}

// ABRConfig holds rendition profiles and GOP settings for media transformation
type ABRConfig struct {
	Renditions      []RenditionProfile
	GOPSize         int
	SegmentDuration int // seconds (standard: 2)
}
