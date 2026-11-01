package storage

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGCSStorageAdapter_GeneratePresignedPutURL(t *testing.T) {
	candidates := []string{
		"../../../../credentials.json",
		"../../../credentials.json",
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
		t.Skip("credentials.json not present, skipping live GCS signature test")
	}

	ctx := context.Background()
	adapter, err := NewGCSStorageAdapter(ctx, credFile, "bitmill-project", "https://storage.googleapis.com/bitmill-processed-videos")
	require.NoError(t, err)
	defer adapter.Close()

	// Test V4 Signed PUT URL Generation
	putURL, err := adapter.GeneratePresignedPutURL(ctx, "bitmill-raw-videos", "test-video/raw.mp4", 15*time.Minute, "video/mp4")
	require.NoError(t, err)
	assert.NotEmpty(t, putURL)

	// Validate V4 Signature parameters
	assert.True(t, strings.HasPrefix(putURL, "https://storage.googleapis.com/bitmill-raw-videos/test-video/raw.mp4"))
	assert.Contains(t, putURL, "X-Goog-Algorithm=GOOG4-RSA-SHA256")
	assert.Contains(t, putURL, "X-Goog-Credential=")
	assert.Contains(t, putURL, "X-Goog-Signature=")

	// Test Download URL Generation
	dlURL, err := adapter.GetDownloadURL(ctx, "bitmill-processed-videos", "videos/123/hls/master.m3u8", 1*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, "https://storage.googleapis.com/bitmill-processed-videos/videos/123/hls/master.m3u8", dlURL)
}
