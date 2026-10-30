package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"quidb.dev/go-streaming/internal/domain/video"
	"quidb.dev/go-streaming/pkg/logger"
)

var _ video.JobQueue = (*RabbitMQAdapter)(nil)

type RabbitMQConfig struct {
	URI           string
	Exchange      string
	Queue         string
	RoutingKey    string
	DLX           string
	DLQ           string
	PrefetchCount int
}

type RabbitMQAdapter struct {
	conn       *amqp.Connection
	pubChannel *amqp.Channel
	cfg        RabbitMQConfig
	log        *logger.Logger
	mu         sync.Mutex
	closed     bool
}

func NewRabbitMQAdapter(cfg RabbitMQConfig, log *logger.Logger) (*RabbitMQAdapter, error) {
	if cfg.URI == "" {
		cfg.URI = "amqp://bitmill_mq:bitmill_mq_password@localhost:5672/"
	}
	if cfg.Exchange == "" {
		cfg.Exchange = "bitmill.video.events"
	}
	if cfg.Queue == "" {
		cfg.Queue = "video.transcoding.jobs"
	}
	if cfg.RoutingKey == "" {
		cfg.RoutingKey = "video.transcode.job"
	}
	if cfg.DLX == "" {
		cfg.DLX = "bitmill.video.dlx"
	}
	if cfg.DLQ == "" {
		cfg.DLQ = "video.transcoding.dlq"
	}
	if cfg.PrefetchCount <= 0 {
		cfg.PrefetchCount = 1
	}

	conn, err := amqp.Dial(cfg.URI)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to open AMQP channel: %w", err)
	}

	// Enable Publisher Confirms on publish channel
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("failed to put channel into confirm mode: %w", err)
	}

	adapter := &RabbitMQAdapter{
		conn:       conn,
		pubChannel: ch,
		cfg:        cfg,
		log:        log,
	}

	// Declare Exchanges, Queues, DLX, DLQ, and TTL Retry queue
	if err := adapter.setupTopology(); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("failed to declare AMQP topology: %w", err)
	}

	return adapter, nil
}

const (
	RetryExchange   = "bitmill.video.retry"
	RetryQueue30s   = "video.transcoding.retry.30s"
	RetryRoutingKey = "video.transcode.retry"
)

func (r *RabbitMQAdapter) setupTopology() error {
	ch := r.pubChannel

	// 1. Declare Primary Topic Exchange
	if err := ch.ExchangeDeclare(
		r.cfg.Exchange,
		"topic",
		true,  // durable
		false, // auto-deleted
		false, // internal
		false, // no-wait
		nil,   // arguments
	); err != nil {
		return fmt.Errorf("failed to declare exchange %s: %w", r.cfg.Exchange, err)
	}

	// 2. Declare Dead Letter Exchange (DLX)
	if err := ch.ExchangeDeclare(
		r.cfg.DLX,
		"direct",
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("failed to declare DLX %s: %w", r.cfg.DLX, err)
	}

	// 3. Declare Dead Letter Queue (DLQ)
	if _, err := ch.QueueDeclare(
		r.cfg.DLQ,
		true,  // durable
		false, // auto-delete
		false, // exclusive
		false, // no-wait
		nil,
	); err != nil {
		return fmt.Errorf("failed to declare DLQ %s: %w", r.cfg.DLQ, err)
	}

	// Bind DLQ to DLX
	if err := ch.QueueBind(
		r.cfg.DLQ,
		r.cfg.DLQ, // routing key = dlq name
		r.cfg.DLX,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("failed to bind DLQ to DLX: %w", err)
	}

	// 4. Declare Retry Exchange & 30s TTL Retry Queue
	if err := ch.ExchangeDeclare(
		RetryExchange,
		"direct",
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("failed to declare retry exchange: %w", err)
	}

	retryArgs := amqp.Table{
		"x-message-ttl":             int32(30000), // 30 seconds
		"x-dead-letter-exchange":    r.cfg.Exchange,
		"x-dead-letter-routing-key": r.cfg.RoutingKey,
	}
	if _, err := ch.QueueDeclare(
		RetryQueue30s,
		true,
		false,
		false,
		false,
		retryArgs,
	); err != nil {
		return fmt.Errorf("failed to declare retry queue: %w", err)
	}

	if err := ch.QueueBind(
		RetryQueue30s,
		RetryRoutingKey,
		RetryExchange,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("failed to bind retry queue: %w", err)
	}

	// 5. Declare Primary Job Queue with DLX forwarding
	queueArgs := amqp.Table{
		"x-dead-letter-exchange":    r.cfg.DLX,
		"x-dead-letter-routing-key": r.cfg.DLQ,
	}
	if _, err := ch.QueueDeclare(
		r.cfg.Queue,
		true,  // durable
		false, // auto-delete
		false, // exclusive
		false, // no-wait
		queueArgs,
	); err != nil {
		return fmt.Errorf("failed to declare primary queue %s: %w", r.cfg.Queue, err)
	}

	// Bind primary queue to primary exchange
	if err := ch.QueueBind(
		r.cfg.Queue,
		r.cfg.RoutingKey,
		r.cfg.Exchange,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("failed to bind primary queue: %w", err)
	}

	r.log.Info("RabbitMQ AMQP topology declared successfully (Exchange: %s, Queue: %s, DLQ: %s)", r.cfg.Exchange, r.cfg.Queue, r.cfg.DLQ)
	return nil
}

// PublishTranscodeJob publishes a persistent message with publisher confirms
func (r *RabbitMQAdapter) PublishTranscodeJob(ctx context.Context, job *video.TranscodeJobMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return errors.New("rabbitmq adapter is closed")
	}

	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("failed to marshal transcode job message: %w", err)
	}

	confirms := r.pubChannel.NotifyPublish(make(chan amqp.Confirmation, 1))

	err = r.pubChannel.PublishWithContext(
		ctx,
		r.cfg.Exchange,
		r.cfg.RoutingKey,
		true,  // mandatory
		false, // immediate
		amqp.Publishing{
			DeliveryMode: amqp.Persistent,
			ContentType:  "application/json",
			Timestamp:    time.Now(),
			MessageId:    job.EventID,
			Body:         body,
			Headers: amqp.Table{
				"video_id":      job.VideoID,
				"job_id":        job.JobID,
				"attempt_count": job.AttemptCount,
			},
		},
	)
	if err != nil {
		return fmt.Errorf("failed to publish transcode job: %w", err)
	}

	select {
	case confirmed := <-confirms:
		if !confirmed.Ack {
			return fmt.Errorf("failed to receive publisher ack from rabbitmq for event %s", job.EventID)
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return errors.New("timeout awaiting rabbitmq publisher confirm")
	}

	r.log.Info("Published transcode job successfully (VideoID=%s, JobID=%s, Attempt=%d)", job.VideoID, job.JobID, job.AttemptCount)
	return nil
}

// PublishToRetryQueue routes a message into the 30s TTL delayed queue
func (r *RabbitMQAdapter) PublishToRetryQueue(ctx context.Context, job *video.TranscodeJobMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return errors.New("rabbitmq adapter is closed")
	}

	body, err := json.Marshal(job)
	if err != nil {
		return err
	}

	confirms := r.pubChannel.NotifyPublish(make(chan amqp.Confirmation, 1))

	err = r.pubChannel.PublishWithContext(
		ctx,
		RetryExchange,
		RetryRoutingKey,
		true,
		false,
		amqp.Publishing{
			DeliveryMode: amqp.Persistent,
			ContentType:  "application/json",
			Timestamp:    time.Now(),
			MessageId:    job.EventID,
			Body:         body,
			Headers: amqp.Table{
				"video_id":      job.VideoID,
				"job_id":        job.JobID,
				"attempt_count": job.AttemptCount,
			},
		},
	)
	if err != nil {
		return err
	}

	select {
	case confirmed := <-confirms:
		if !confirmed.Ack {
			return errors.New("nack received on retry queue publish")
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return errors.New("timeout awaiting retry publisher confirm")
	}

	r.log.Info("Job queued for 30s delayed retry (VideoID=%s, NextAttempt=%d)", job.VideoID, job.AttemptCount)
	return nil
}

// ConsumeTranscodeJobs runs consumer with manual ACK and strict prefetch
func (r *RabbitMQAdapter) ConsumeTranscodeJobs(ctx context.Context, handler func(ctx context.Context, job *video.TranscodeJobMessage) error) error {
	consumeChannel, err := r.conn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open consume channel: %w", err)
	}
	defer consumeChannel.Close()

	// Strict Prefetch: only pull 1 unacked job per worker slot
	if err := consumeChannel.Qos(r.cfg.PrefetchCount, 0, false); err != nil {
		return fmt.Errorf("failed to set prefetch QoS: %w", err)
	}

	consumerTag := fmt.Sprintf("worker-%d", time.Now().UnixNano())
	msgs, err := consumeChannel.Consume(
		r.cfg.Queue,
		consumerTag, // explicit consumer tag for cancellation
		false,       // autoAck = FALSE (strict manual ACK)
		false,       // exclusive
		false,       // no-local
		false,       // no-wait
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to start consuming from queue %s: %w", r.cfg.Queue, err)
	}

	r.log.Info("Worker consumer started listening on queue %s (Prefetch=%d, Tag=%s)", r.cfg.Queue, r.cfg.PrefetchCount, consumerTag)

	// Monitor context cancellation to cancel consumer tag (stop pulling new messages)
	stopChan := make(chan struct{})
	defer close(stopChan)

	go func() {
		select {
		case <-ctx.Done():
			r.log.Warn("Termination context triggered: cancelling AMQP consumer to halt new message delivery...")
			_ = consumeChannel.Cancel(consumerTag, false)
		case <-stopChan:
			return
		}
	}()

	for {
		select {
		case <-ctx.Done():
			r.log.Info("Consumer context cancelled, shutting down consumption loop")
			return ctx.Err()
		case d, ok := <-msgs:
			if !ok {
				r.log.Warn("AMQP deliveries channel closed (consumer drained cleanly)")
				return nil
			}

			var job video.TranscodeJobMessage
			if err := json.Unmarshal(d.Body, &job); err != nil {
				r.log.Error("Malformed job payload (unparsable JSON), sending straight to DLQ: %v", err)
				_ = d.Nack(false, false) // requeue = false -> DLQ
				continue
			}

			r.log.Info("Received transcode job from queue (VideoID=%s, JobID=%s, Attempt=%d/%d)",
				job.VideoID, job.JobID, job.AttemptCount, job.MaxAttempts)

			// Execute handler with grace timeout for active job (110s max)
			jobCtx, jobCancel := context.WithTimeout(context.Background(), 110*time.Second)
			jobErr := handler(jobCtx, &job)
			jobCancel()

			if jobErr == nil {
				// Success: Manual Ack
				if err := d.Ack(false); err != nil {
					r.log.Error("Failed to ACK message: %v", err)
				} else {
					r.log.Info("Successfully acknowledged job (VideoID=%s, JobID=%s)", job.VideoID, job.JobID)
				}
				continue
			}

			// If job timed out because of worker shutdown deadline, requeue message safely
			if errors.Is(jobErr, context.DeadlineExceeded) || errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
				r.log.Warn("Job processing deadline exceeded during shutdown, nacking with requeue=true: VideoID=%s", job.VideoID)
				_ = d.Nack(false, true)
				return ctx.Err()
			}

			// Handle Failure:
			r.log.Error("Job processing failed for VideoID=%s: %v", job.VideoID, jobErr)

			// Check if fatal non-retriable error OR attempts exhausted
			if job.AttemptCount >= job.MaxAttempts || isPermanentFailure(jobErr) {
				r.log.Warn("Job reached max attempts (%d/%d) or permanent failure, quarantining to DLQ: VideoID=%s",
					job.AttemptCount, job.MaxAttempts, job.VideoID)
				_ = d.Nack(false, false) // requeue = false -> moves to DLX / DLQ
			} else {
				// Retryable: Schedule for 30s delay retry via TTL queue
				job.AttemptCount++
				retryErr := r.PublishToRetryQueue(ctx, &job)
				if retryErr != nil {
					r.log.Error("Failed to schedule retry queue message, nacking with requeue: %v", retryErr)
					_ = d.Nack(false, true) // requeue locally as fallback
				} else {
					// Ack original message since retry is safely staged in TTL queue
					_ = d.Ack(false)
				}
			}
		}
	}
}

// isPermanentFailure checks if an error is non-recoverable (poison pill, bad codec, etc.)
func isPermanentFailure(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return errors.Is(err, video.ErrInvalidInput) ||
		errors.Is(err, video.ErrVideoNotFound) ||
		len(s) > 0 && (containsAny(s, "corrupt", "unsupported", "invalid format", "moov atom not found", "no streams found", "permanent"))
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// Close gracefully closes channel and connection
func (r *RabbitMQAdapter) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil
	}
	r.closed = true

	var errs []error
	if r.pubChannel != nil {
		if err := r.pubChannel.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.conn != nil {
		if err := r.conn.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing rabbitmq adapter: %v", errs)
	}
	return nil
}
