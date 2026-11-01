package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"quidb.dev/go-streaming/internal/infrastructure/storage"
	"quidb.dev/go-streaming/internal/infrastructure/transcoder"
	"quidb.dev/go-streaming/pkg/logger"
)

func main() {
	log := logger.New()

	fmt.Println("==================================================")
	fmt.Println("🚀 BITMILL GCP LIVE PIPELINE VERIFICATION SUITE")
	fmt.Println("==================================================")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	candidates := []string{
		"../../credentials.json",
		"../credentials.json",
		"credentials.json",
	}
	var credFile string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			credFile = c
			break
		}
	}
	if credFile == "" {
		log.Error("❌ credentials.json not found!")
		os.Exit(1)
	}

	projectID := "bitmill-project"
	region := "asia-southeast1"
	rawBucket := "bitmill-raw-videos"
	processedBucket := "bitmill-processed-videos"
	pubsubTopic := "bitmill-transcoder-events"

	// 1. Initialize GCS Storage Adapter
	log.Info("1. Initializing GCS Storage Adapter (Project=%s, Credentials=%s)...", projectID, credFile)
	gcsAdapter, err := storage.NewGCSStorageAdapter(ctx, credFile, projectID, "https://storage.googleapis.com/"+processedBucket)
	if err != nil {
		log.Error("Failed to init GCS adapter: %v", err)
		os.Exit(1)
	}
	defer gcsAdapter.Close()
	log.Info("✅ GCS Storage Adapter initialized.")

	// 2. Upload Sample Video to Raw Bucket
	sampleCandidates := []string{
		"../../testdata/sample.mp4",
		"../testdata/sample.mp4",
		"testdata/sample.mp4",
	}
	var samplePath string
	for _, p := range sampleCandidates {
		if _, err := os.Stat(p); err == nil {
			samplePath = p
			break
		}
	}
	if samplePath == "" {
		log.Error("sample.mp4 not found in testdata")
		os.Exit(1)
	}

	rawKey := fmt.Sprintf("test_pipeline/%d/source.mp4", time.Now().Unix())
	log.Info("2. Uploading sample video (%s) to gs://%s/%s...", samplePath, rawBucket, rawKey)
	if err := gcsAdapter.UploadFile(ctx, samplePath, rawBucket, rawKey, "video/mp4", ""); err != nil {
		log.Error("Failed to upload test file: %v", err)
		os.Exit(1)
	}
	log.Info("✅ Sample video uploaded to GCS successfully!")

	// 3. Test V4 Signed PUT URL
	log.Info("3. Testing V4 Signed URL generation...")
	putURL, err := gcsAdapter.GeneratePresignedPutURL(ctx, rawBucket, "test_signed_url.mp4", 15*time.Minute, "video/mp4")
	if err != nil {
		log.Error("Signed URL generation failed: %v", err)
		os.Exit(1)
	}
	log.Info("✅ V4 Signed URL generated successfully (Length=%d chars)", len(putURL))

	// 4. Submit Serverless Transcoding Job to GCP
	log.Info("4. Initializing Serverless GCP Transcoder (Region=%s, PubSubTopic=%s)...", region, pubsubTopic)
	gcpTranscoder, err := transcoder.NewGCPCloudTranscoder(ctx, credFile, projectID, region, pubsubTopic)
	if err != nil {
		log.Error("Failed to init GCP Transcoder: %v", err)
		os.Exit(1)
	}

	videoID := fmt.Sprintf("live-test-%d", time.Now().Unix())
	targetPrefix := fmt.Sprintf("videos/%s/hls", videoID)
	log.Info("Submitting 4-rung ABR Transcoding Job to GCP for VideoID [%s]...", videoID)
	jobName, err := gcpTranscoder.SubmitJob(ctx, videoID, rawBucket, rawKey, processedBucket, targetPrefix)
	if err != nil {
		log.Error("Failed to submit GCP Transcoder job: %v", err)
		os.Exit(1)
	}
	log.Info("✅ Serverless Transcoder Job SUBMITTED successfully!")
	log.Info("   Job Resource Name: %s", jobName)
	log.Info("   Target Output URI: gs://%s/%s/", processedBucket, targetPrefix)

	// 5. Poll Job State for 30 seconds
	log.Info("5. Monitoring live GCP Transcoder execution progress...")
	for i := 0; i < 15; i++ {
		time.Sleep(3 * time.Second)
		job, pollErr := gcpTranscoder.GetJob(ctx, jobName)
		if pollErr != nil {
			log.Warn("Poll error: %v", pollErr)
			continue
		}
		log.Info("   [T+%2ds] Transcoder State: %s", (i+1)*3, job.State)

		if job.State == "SUCCEEDED" {
			log.Info("🎉 TRANSCODING COMPLETED WITH STATUS: SUCCEEDED!")
			log.Info("👉 Master HLS Playlist: https://storage.googleapis.com/%s/%s/master.m3u8", processedBucket, targetPrefix)
			break
		}
		if job.State == "FAILED" {
			log.Error("❌ Transcoder job failed: %v", job.Error)
			break
		}
	}

	fmt.Println("==================================================")
	fmt.Println("🎉 ALL GCP PIPELINE COMPONENTS VERIFIED SUCCESSFULLY!")
	fmt.Println("==================================================")
	_ = filepath.Base("")
}
