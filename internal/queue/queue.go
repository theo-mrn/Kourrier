// Package queue defines the asynchronous job queue between the API and the
// workers (ADR 002, ADR 006). RedisStreams is the reference implementation.
package queue

import (
	"context"
	"time"

	"github.com/theo-mrn/kourrier/internal/mail"
)

// Job is the unit of work stored in the queue. It is self-contained: a worker
// needs nothing else to render and send the e-mail.
type Job struct {
	ID         string       `json:"id"`
	TenantID   string       `json:"tenant_id"`
	Attempts   int          `json:"attempts"`
	EnqueuedAt time.Time    `json:"enqueued_at"`
	Request    mail.Request `json:"request"`
}

// Delivery is a job handed to a worker. It must be settled with exactly one of
// Ack, Retry or DeadLetter; otherwise it is redelivered to another consumer
// once the visibility timeout expires.
type Delivery struct {
	Job *Job
	// ID is the backend-specific message identifier.
	ID string
}

// Stats is a snapshot of the queue size by state.
type Stats struct {
	Ready   int64 // waiting to be picked up
	Pending int64 // picked up, not settled yet
	Delayed int64 // waiting for a retry
	Dead    int64 // in the dead-letter queue
}

// Queue is the contract between the API, the workers and a queue backend.
type Queue interface {
	Enqueue(ctx context.Context, job *Job) error
	// Receive blocks until a job is available, the backend's poll timeout
	// expires (nil, nil) or ctx is done.
	Receive(ctx context.Context) (*Delivery, error)
	// Ack removes a successfully processed job.
	Ack(ctx context.Context, d *Delivery) error
	// Retry reschedules the job (with its updated Attempts) for at.
	Retry(ctx context.Context, d *Delivery, at time.Time) error
	// DeadLetter moves the job to the dead-letter queue.
	DeadLetter(ctx context.Context, d *Delivery, reason string) error
	Stats(ctx context.Context) (Stats, error)
}
