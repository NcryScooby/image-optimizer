package db

import (
	"context"
	"testing"
)

func TestStorePing_Nil(t *testing.T) {
	var s *Store
	if err := s.Ping(context.Background()); err == nil {
		t.Fatal("expected error for nil store")
	}

	s = &Store{}
	if err := s.Ping(context.Background()); err == nil {
		t.Fatal("expected error for store without pool")
	}
}
