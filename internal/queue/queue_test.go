package queue

import (
	"context"
	"testing"
)

func TestQueueInspect_NilClient(t *testing.T) {
	var c *Client
	_, err := c.QueueInspect()
	if err == nil {
		t.Fatal("expected error for nil client")
	}

	c = &Client{}
	_, err = c.QueueInspect()
	if err == nil {
		t.Fatal("expected error for disconnected client")
	}
}

func TestPing_NilClient(t *testing.T) {
	var c *Client
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected error for nil client")
	}

	c = &Client{}
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected error for disconnected client")
	}
}
