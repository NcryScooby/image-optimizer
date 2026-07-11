package queue

import (
	"context"
	"encoding/json"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// QueueName is the RabbitMQ queue for variant processing jobs.
const QueueName = "image.variants"

// Job is the JSON payload published to image.variants.
type Job struct {
	VariantID string `json:"variant_id"`
}

// Handler processes a variant job. Return nil to ack; non-nil to nack+requeue.
type Handler func(ctx context.Context, variantID string) error

// Client wraps an AMQP connection/channel for publish and consume.
type Client struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

// Connect dials RABBITMQ_URL, opens a channel, and declares image.variants.
func Connect(url string) (*Client, error) {
	if url == "" {
		return nil, fmt.Errorf("queue: RABBITMQ_URL is required")
	}
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("queue: dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("queue: channel: %w", err)
	}
	if _, err := ch.QueueDeclare(
		QueueName,
		true,  // durable
		false, // autoDelete
		false, // exclusive
		false, // noWait
		nil,
	); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("queue: declare %s: %w", QueueName, err)
	}
	return &Client{conn: conn, ch: ch}, nil
}

// QueueInspect returns the approximate number of ready messages in image.variants.
func (c *Client) QueueInspect() (int, error) {
	if c == nil || c.ch == nil {
		return 0, fmt.Errorf("queue: client not connected")
	}
	q, err := c.ch.QueueInspect(QueueName)
	if err != nil {
		return 0, fmt.Errorf("queue: inspect %s: %w", QueueName, err)
	}
	return q.Messages, nil
}

// Publish enqueues a variant processing job.
func (c *Client) Publish(ctx context.Context, variantID string) error {
	if c == nil || c.ch == nil {
		return fmt.Errorf("queue: client not connected")
	}
	if variantID == "" {
		return fmt.Errorf("queue: variant_id is required")
	}
	body, err := json.Marshal(Job{VariantID: variantID})
	if err != nil {
		return fmt.Errorf("queue: marshal job: %w", err)
	}
	if err := c.ch.PublishWithContext(ctx,
		"",        // default exchange
		QueueName, // routing key = queue name
		false,     // mandatory
		false,     // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
		},
	); err != nil {
		return fmt.Errorf("queue: publish: %w", err)
	}
	return nil
}

// Consume starts a consumer with manual ack. Blocks until ctx is cancelled
// or the channel closes. On handler error the message is nack'd and requeued;
// on success it is acked. Malformed payloads are nack'd without requeue.
func (c *Client) Consume(ctx context.Context, handler Handler) error {
	if c == nil || c.ch == nil {
		return fmt.Errorf("queue: client not connected")
	}
	if handler == nil {
		return fmt.Errorf("queue: handler is required")
	}
	if err := c.ch.Qos(1, 0, false); err != nil {
		return fmt.Errorf("queue: qos: %w", err)
	}

	deliveries, err := c.ch.Consume(
		QueueName,
		"",    // consumer tag
		false, // autoAck
		false, // exclusive
		false, // noLocal
		false, // noWait
		nil,
	)
	if err != nil {
		return fmt.Errorf("queue: consume: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("queue: deliveries channel closed")
			}
			c.handleDelivery(ctx, d, handler)
		}
	}
}

func (c *Client) handleDelivery(ctx context.Context, d amqp.Delivery, handler Handler) {
	var job Job
	if err := json.Unmarshal(d.Body, &job); err != nil || job.VariantID == "" {
		_ = d.Nack(false, false) // drop poison
		return
	}

	if err := handler(ctx, job.VariantID); err != nil {
		_ = d.Nack(false, true) // requeue for retry
		return
	}
	_ = d.Ack(false)
}

// Close closes the channel and connection.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	var first error
	if c.ch != nil {
		if err := c.ch.Close(); err != nil && first == nil {
			first = err
		}
	}
	if c.conn != nil {
		if err := c.conn.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
