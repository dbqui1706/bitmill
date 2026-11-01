package transcoder

import (
	"context"
	"fmt"
	"strings"

	transcoderAPI "google.golang.org/api/transcoder/v1"
	"google.golang.org/api/option"
)

// GCPCloudTranscoder handles serverless media encoding jobs via Google Cloud Transcoder API
type GCPCloudTranscoder struct {
	service     *transcoderAPI.Service
	projectID   string
	region      string
	pubsubTopic string
}

// NewGCPCloudTranscoder initializes the Google Cloud Transcoder API client
func NewGCPCloudTranscoder(ctx context.Context, credentialsFile, projectID, region, pubsubTopic string) (*GCPCloudTranscoder, error) {
	var opts []option.ClientOption
	if credentialsFile != "" {
		opts = append(opts, option.WithCredentialsFile(credentialsFile))
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
func (g *GCPCloudTranscoder) SubmitJob(ctx context.Context, videoID, sourceBucket, sourceKey, targetBucket, targetPrefix string) (string, error) {
	parent := fmt.Sprintf("projects/%s/locations/%s", g.projectID, g.region)

	inputURI := fmt.Sprintf("gs://%s/%s", sourceBucket, strings.TrimPrefix(sourceKey, "/"))
	outputURI := fmt.Sprintf("gs://%s/%s/", targetBucket, strings.Trim(targetPrefix, "/"))

	job := &transcoderAPI.Job{
		InputUri:  inputURI,
		OutputUri: outputURI,
		Labels: map[string]string{
			"video_id": videoID,
		},
		Config: &transcoderAPI.JobConfig{
			ElementaryStreams: []*transcoderAPI.ElementaryStream{
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
				{
					Key: "audio_aac",
					AudioStream: &transcoderAPI.AudioStream{
						Codec:           "aac",
						BitrateBps:      128000,
						ChannelCount:    2,
						SampleRateHertz: 48000,
					},
				},
			},
			MuxStreams: []*transcoderAPI.MuxStream{
				{
					Key:               "hls_1080p",
					FileName:          "1080p.m3u8",
					Container:         "ts",
					ElementaryStreams: []string{"video_1080p", "audio_aac"},
					SegmentSettings: &transcoderAPI.SegmentSettings{
						SegmentDuration: "2s",
					},
				},
				{
					Key:               "hls_720p",
					FileName:          "720p.m3u8",
					Container:         "ts",
					ElementaryStreams: []string{"video_720p", "audio_aac"},
					SegmentSettings: &transcoderAPI.SegmentSettings{
						SegmentDuration: "2s",
					},
				},
				{
					Key:               "hls_480p",
					FileName:          "480p.m3u8",
					Container:         "ts",
					ElementaryStreams: []string{"video_480p", "audio_aac"},
					SegmentSettings: &transcoderAPI.SegmentSettings{
						SegmentDuration: "2s",
					},
				},
				{
					Key:               "hls_360p",
					FileName:          "360p.m3u8",
					Container:         "ts",
					ElementaryStreams: []string{"video_360p", "audio_aac"},
					SegmentSettings: &transcoderAPI.SegmentSettings{
						SegmentDuration: "2s",
					},
				},
			},
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
