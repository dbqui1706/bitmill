package video

import "context"

// Repository định nghĩa các hành vi lưu trữ Video Entity (Không quan tâm là lưu vào RAM, SQL hay NoSQL)
type Repository interface {
	Save(ctx context.Context, v *Video) error
	FindByID(ctx context.Context, id string) (*Video, error)
	List(ctx context.Context) ([]*Video, error)
}

// Transcoder định nghĩa hành vi biến đổi video thô thành các đoạn HLS (Không quan tâm dùng FFmpeg, AWS Elemental hay tool nào)
type Transcoder interface {
	TranscodeToHLS(ctx context.Context, sourcePath, outputDir, playlistName string) error
}
