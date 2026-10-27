package repository

import (
	"context"
	"sync"

	"quidb.dev/go-streaming/internal/domain/video"
)

// MemoryRepo lưu trữ Entity trong bộ nhớ RAM, bảo vệ bằng Mutex chống xung đột Race Condition
type MemoryRepo struct {
	mu     sync.RWMutex
	videos map[string]*video.Video
}

func NewMemoryRepo() *MemoryRepo {
	return &MemoryRepo{
		videos: make(map[string]*video.Video),
	}
}

func (r *MemoryRepo) Save(ctx context.Context, v *video.Video) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.videos[v.ID] = v
	return nil
}

func (r *MemoryRepo) FindByID(ctx context.Context, id string) (*video.Video, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, exists := r.videos[id]
	if !exists {
		return nil, video.ErrVideoNotFound
	}
	return v, nil
}

func (r *MemoryRepo) List(ctx context.Context) ([]*video.Video, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	res := make([]*video.Video, 0, len(r.videos))
	for _, v := range r.videos {
		res = append(res, v)
	}
	return res, nil
}

func (r *MemoryRepo) UpdateStatus(ctx context.Context, id string, status video.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, exists := r.videos[id]
	if !exists {
		return video.ErrVideoNotFound
	}
	v.Status = status
	return nil
}

func (r *MemoryRepo) UpdateStatusIf(ctx context.Context, id string, newStatus video.Status, currentStatus video.Status) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, exists := r.videos[id]
	if !exists {
		return false, video.ErrVideoNotFound
	}
	if v.Status == currentStatus {
		v.Status = newStatus
		return true, nil
	}
	return false, nil
}

