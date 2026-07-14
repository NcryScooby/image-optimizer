package storage

import (
	"path"
	"testing"
)

func TestNormalizeFolderAndOriginalKey(t *testing.T) {
	folder, err := normalizeFolder("storely/1/catalog")
	if err != nil {
		t.Fatalf("normalizeFolder: %v", err)
	}
	key := path.Join(folder, "11111111-1111-1111-1111-111111111111.jpg")
	want := "storely/1/catalog/11111111-1111-1111-1111-111111111111.jpg"
	if key != want {
		t.Fatalf("key = %q, want %q", key, want)
	}
}

func TestNormalizeFolder_RejectsEscape(t *testing.T) {
	if _, err := normalizeFolder("../etc"); err == nil {
		t.Fatal("expected error")
	}
}
