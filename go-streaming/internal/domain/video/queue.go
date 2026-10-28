package video

import (
	"context"
)

// JobQueue defines the domain contract for asynchronous message brokering (RabbitMQ / AMQP 0-9-1).
// High-level application logic depends exclusively on this abstraction (IoC).
type JobQueue interface {
	// PublishTranscodeJob publishes a persistent video transcoding job message with publisher confirms
	PublishTranscodeJob(ctx context.Context, job *TranscodeJobMessage) error

	// ConsumeTranscodeJobs starts a consumer worker with manual ACK and prefetch throttling
	ConsumeTranscodeJobs(ctx context.Context, handler func(ctx context.Context, job *TranscodeJobMessage) error) error

	// Close gracefully disconnects channels and AMQP connections
	Close() error
}
