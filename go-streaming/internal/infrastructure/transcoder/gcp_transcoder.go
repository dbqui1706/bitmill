package transcoder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	transcoderAPI "google.golang.org/api/transcoder/v1"
	"google.golang.org/api/option"
	"quidb.dev/go-streaming/internal/domain/video"
)

// GCPCloudTranscoder handles serverless media encoding jobs via Google Cloud Transcoder API
type GCPCloudTranscoder struct {
	service     *transcoderAPI.Service
	projectID   string
	region      string
	pubsubTopic string
}

func resolveCredentialsPath(path string) string {
	if path == "" {
		return ""
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if !filepath.IsAbs(path) {
		fallback := filepath.Join("..", path)
		if _, err := os.Stat(fallback); err == nil {
			return fallback
		}
	}
	return path
}

func loadTranscoderCredentials(credentialsFile string) (option.ClientOption, string, error) {
	// 1. Priority 1: In-Memory JSON from Environment Variable (Zero File on Disk)
	if envJSON := os.Getenv("GCP_CREDENTIALS_JSON"); strings.TrimSpace(envJSON) != "" {
		var sa struct {
			ProjectID string `json:"project_id"`
		}
		_ = json.Unmarshal([]byte(envJSON), &sa)
		return option.WithCredentialsJSON([]byte(envJSON)), sa.ProjectID, nil
	}

	// 2. Priority 2: Standard Google Cloud Environment Variable or Config Path
	targetPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if targetPath == "" {
		targetPath = credentialsFile
	}

	if targetPath != "" {
		resolvedPath := resolveCredentialsPath(targetPath)
		if data, err := os.ReadFile(resolvedPath); err == nil {
			var sa struct {
				ProjectID string `json:"project_id"`
			}
			_ = json.Unmarshal(data, &sa)
			return option.WithCredentialsFile(resolvedPath), sa.ProjectID, nil
		}
	}

	// 3. Priority 3: Fallback to ADC
	return nil, "", nil
}

// NewGCPCloudTranscoder initializes the Google Cloud Transcoder API client
func NewGCPCloudTranscoder(ctx context.Context, credentialsFile, projectID, region, pubsubTopic string) (*GCPCloudTranscoder, error) {
	var opts []option.ClientOption

	credOpt, credProject, err := loadTranscoderCredentials(credentialsFile)
	if err != nil {
		return nil, err
	}
	if credOpt != nil {
		opts = append(opts, credOpt)
		if projectID == "" {
			projectID = credProject
		}
	}

	service, err := transcoderAPI.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Google Cloud Transcoder API service: %w", err)
	}

	if region == "" {
		region = "asia-southeast1"
	}

	return &GCPCloudTranscoder{
		service:     service,
		projectID:   projectID,
		region:      region,
		pubsubTopic: pubsubTopic,
	}, nil
}

// SubmitJob creates and schedules an asynchronous serverless 4-rung ABR HLS transcoding job on Google Cloud
func (g *GCPCloudTranscoder) SubmitJob(ctx context.Context, videoID, sourceBucket, sourceKey, targetBucket, targetPrefix string, hasAudio bool) (string, error) {
	parent := fmt.Sprintf("projects/%s/locations/%s", g.projectID, g.region)

	inputURI := fmt.Sprintf("gs://%s/%s", sourceBucket, strings.TrimPrefix(sourceKey, "/"))
	outputURI := fmt.Sprintf("gs://%s/%s/", targetBucket, strings.Trim(targetPrefix, "/"))

	elementaryStreams := []*transcoderAPI.ElementaryStream{
		{
			Key: "video_1080p",
			VideoStream: &transcoderAPI.VideoStream{
				H264: &transcoderAPI.H264CodecSettings{
					HeightPixels: 1080,
					WidthPixels:  1920,
					BitrateBps:   4500000,
					FrameRate:    30,
					GopDuration:  "2s",
					Profile:      "high",
					Preset:       "veryfast",
				},
			},
		},
		{
			Key: "video_720p",
			VideoStream: &transcoderAPI.VideoStream{
				H264: &transcoderAPI.H264CodecSettings{
					HeightPixels: 720,
					WidthPixels:  1280,
					BitrateBps:   2500000,
					FrameRate:    30,
					GopDuration:  "2s",
					Profile:      "main",
					Preset:       "veryfast",
				},
			},
		},
		{
			Key: "video_480p",
			VideoStream: &transcoderAPI.VideoStream{
				H264: &transcoderAPI.H264CodecSettings{
					HeightPixels: 480,
					WidthPixels:  854,
					BitrateBps:   1000000,
					FrameRate:    30,
					GopDuration:  "2s",
					Profile:      "main",
					Preset:       "veryfast",
				},
			},
		},
		{
			Key: "video_360p",
			VideoStream: &transcoderAPI.VideoStream{
				H264: &transcoderAPI.H264CodecSettings{
					HeightPixels: 360,
					WidthPixels:  640,
					BitrateBps:   600000,
					FrameRate:    30,
					GopDuration:  "2s",
					Profile:      "baseline",
					Preset:       "veryfast",
				},
			},
		},
	}

	if hasAudio {
		elementaryStreams = append(elementaryStreams, &transcoderAPI.ElementaryStream{
			Key: "audio_aac",
			AudioStream: &transcoderAPI.AudioStream{
				Codec:           "aac",
				BitrateBps:      128000,
				ChannelCount:    2,
				SampleRateHertz: 48000,
			},
		})
	}

	ladderConfigs := []struct {
		key      string
		fileName string
		vKey     string
	}{
		{"hls_1080p", "1080p.m3u8", "video_1080p"},
		{"hls_720p", "720p.m3u8", "video_720p"},
		{"hls_480p", "480p.m3u8", "video_480p"},
		{"hls_360p", "360p.m3u8", "video_360p"},
	}

	muxStreams := make([]*transcoderAPI.MuxStream, 0, len(ladderConfigs))
	for _, cfg := range ladderConfigs {
		streams := []string{cfg.vKey}
		if hasAudio {
			streams = append(streams, "audio_aac")
		}
		muxStreams = append(muxStreams, &transcoderAPI.MuxStream{
			Key:               cfg.key,
			FileName:          cfg.fileName,
			Container:         "ts",
			ElementaryStreams: streams,
			SegmentSettings: &transcoderAPI.SegmentSettings{
				SegmentDuration: "2s",
			},
		})
	}

	job := &transcoderAPI.Job{
		InputUri:  inputURI,
		OutputUri: outputURI,
		Labels: map[string]string{
			"video_id": videoID,
		},
		Config: &transcoderAPI.JobConfig{
			ElementaryStreams: elementaryStreams,
			MuxStreams:        muxStreams,
			Manifests: []*transcoderAPI.Manifest{
				{
					FileName:   "master.m3u8",
					Type:       "HLS",
					MuxStreams: []string{"hls_1080p", "hls_720p", "hls_480p", "hls_360p"},
				},
			},
			SpriteSheets: []*transcoderAPI.SpriteSheet{
				{
					FilePrefix:         "thumbnail",
					Format:             "jpeg",
					SpriteWidthPixels:  640,
					SpriteHeightPixels: 360,
					ColumnCount:        1,
					RowCount:           1,
					TotalCount:         1,
				},
			},
		},
	}

	if g.pubsubTopic != "" {
		topicURI := fmt.Sprintf("projects/%s/topics/%s", g.projectID, g.pubsubTopic)
		job.Config.PubsubDestination = &transcoderAPI.PubsubDestination{
			Topic: topicURI,
		}
	}

	call := g.service.Projects.Locations.Jobs.Create(parent, job)
	createdJob, err := call.Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to create GCP Transcoder job for VideoID %s: %w", videoID, err)
	}

	return createdJob.Name, nil
}

// GetJob retrieves the real-time execution state of a running or completed Transcoder job
func (g *GCPCloudTranscoder) GetJob(ctx context.Context, jobName string) (*transcoderAPI.Job, error) {
	call := g.service.Projects.Locations.Jobs.Get(jobName)
	return call.Context(ctx).Do()
}

// GetJobState retrieves the normalized state and error details of a cloud transcoding job
func (g *GCPCloudTranscoder) GetJobState(ctx context.Context, jobName string) (video.CloudJobState, error) {
	job, err := g.GetJob(ctx, jobName)
	if err != nil {
		return video.CloudJobState{}, err
	}
	errMsg := ""
	if job.Error != nil {
		errMsg = job.Error.Message
	}
	return video.CloudJobState{
		State:        job.State,
		ErrorMessage: errMsg,
	}, nil
}
