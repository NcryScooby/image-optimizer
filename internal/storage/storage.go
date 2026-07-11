package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Storage manages original images and AVIF variants on disk under DATA_DIR.
//
// Layout:
//
//	{root}/originals/{id}.{ext}
//	{root}/variants/{id}/{params_hash}.avif
type Storage struct {
	root string
}

// New creates a Storage rooted at dataDir (typically DATA_DIR, default /data).
// It ensures originals/ and variants/ directories exist.
func New(dataDir string) (*Storage, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("storage: dataDir is required")
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve dataDir: %w", err)
	}
	s := &Storage{root: abs}
	if err := os.MkdirAll(s.originalsDir(), 0o755); err != nil {
		return nil, fmt.Errorf("storage: create originals dir: %w", err)
	}
	if err := os.MkdirAll(s.variantsDir(), 0o755); err != nil {
		return nil, fmt.Errorf("storage: create variants dir: %w", err)
	}
	return s, nil
}

// Root returns the absolute DATA_DIR path.
func (s *Storage) Root() string {
	return s.root
}

// SaveOriginal writes the original image to originals/{id}.{ext}.
// Returns a path relative to DATA_DIR (e.g. "originals/{id}.{ext}").
func (s *Storage) SaveOriginal(ctx context.Context, id, ext string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateID(id); err != nil {
		return "", err
	}
	ext, err := sanitizeExt(ext)
	if err != nil {
		return "", err
	}

	rel := filepath.Join("originals", id+"."+ext)
	abs, err := s.safeJoin(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("storage: mkdir originals: %w", err)
	}
	if err := writeFileAtomic(abs, data, 0o644); err != nil {
		return "", fmt.Errorf("storage: write original: %w", err)
	}
	return filepath.ToSlash(rel), nil
}

// VariantPath returns the relative path for a variant:
// variants/{imageID}/{paramsHash}.avif
func (s *Storage) VariantPath(imageID, paramsHash string) string {
	return filepath.ToSlash(filepath.Join("variants", imageID, paramsHash+".avif"))
}

// WriteVariant writes AVIF bytes to variants/{imageID}/{paramsHash}.avif.
// Returns a path relative to DATA_DIR.
func (s *Storage) WriteVariant(ctx context.Context, imageID, paramsHash string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateID(imageID); err != nil {
		return "", err
	}
	if err := validateParamsHash(paramsHash); err != nil {
		return "", err
	}

	rel := s.VariantPath(imageID, paramsHash)
	abs, err := s.safeJoin(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("storage: mkdir variants: %w", err)
	}
	if err := writeFileAtomic(abs, data, 0o644); err != nil {
		return "", fmt.Errorf("storage: write variant: %w", err)
	}
	return filepath.ToSlash(rel), nil
}

// DeleteImageFiles removes the original file and the entire variants/{imageID}/ directory.
// originalPath may be relative to DATA_DIR or absolute under DATA_DIR.
func (s *Storage) DeleteImageFiles(ctx context.Context, imageID, originalPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(imageID); err != nil {
		return err
	}

	var firstErr error

	if originalPath != "" {
		abs, err := s.resolveUnderRoot(originalPath)
		if err != nil {
			firstErr = err
		} else if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			firstErr = fmt.Errorf("storage: remove original: %w", err)
		}
	}

	variantsAbs, err := s.safeJoin(filepath.Join("variants", imageID))
	if err != nil {
		return err
	}
	if err := os.RemoveAll(variantsAbs); err != nil {
		if firstErr == nil {
			return fmt.Errorf("storage: remove variants: %w", err)
		}
		return fmt.Errorf("storage: remove variants: %w (also: %v)", err, firstErr)
	}
	return firstErr
}

// AbsPath resolves a relative path under DATA_DIR to an absolute path.
// Rejects path traversal outside the root.
func (s *Storage) AbsPath(rel string) (string, error) {
	return s.safeJoin(rel)
}

// ReadFile reads a file by relative or absolute path under DATA_DIR.
func (s *Storage) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := s.resolveUnderRoot(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("storage: read file: %w", err)
	}
	return data, nil
}

// Open opens a file by relative or absolute path under DATA_DIR for streaming.
// Caller must Close the returned file.
func (s *Storage) Open(path string) (*os.File, error) {
	abs, err := s.resolveUnderRoot(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("storage: open file: %w", err)
	}
	return f, nil
}

func (s *Storage) originalsDir() string {
	return filepath.Join(s.root, "originals")
}

func (s *Storage) variantsDir() string {
	return filepath.Join(s.root, "variants")
}

// safeJoin joins rel to root and ensures the result stays under root.
func (s *Storage) safeJoin(rel string) (string, error) {
	rel = filepath.Clean("/" + filepath.ToSlash(rel))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || rel == "." {
		return "", fmt.Errorf("storage: empty path")
	}
	abs := filepath.Join(s.root, rel)
	return s.ensureUnderRoot(abs)
}

// resolveUnderRoot accepts a relative path or an absolute path already under root.
func (s *Storage) resolveUnderRoot(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("storage: empty path")
	}
	if filepath.IsAbs(path) {
		return s.ensureUnderRoot(filepath.Clean(path))
	}
	return s.safeJoin(path)
}

func (s *Storage) ensureUnderRoot(abs string) (string, error) {
	abs = filepath.Clean(abs)
	root := s.root
	sep := string(os.PathSeparator)
	if abs != root && !strings.HasPrefix(abs, root+sep) {
		return "", fmt.Errorf("storage: path escapes data dir")
	}
	return abs, nil
}

func validateID(id string) error {
	if id == "" {
		return fmt.Errorf("storage: id is required")
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("storage: invalid id")
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("storage: invalid id")
	}
	return nil
}

func validateParamsHash(h string) error {
	if h == "" {
		return fmt.Errorf("storage: paramsHash is required")
	}
	if strings.ContainsAny(h, `/\`) || strings.Contains(h, "..") {
		return fmt.Errorf("storage: invalid paramsHash")
	}
	for _, r := range h {
		if (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') || (r >= '0' && r <= '9') {
			continue
		}
		return fmt.Errorf("storage: invalid paramsHash")
	}
	return nil
}

func sanitizeExt(ext string) (string, error) {
	ext = strings.TrimPrefix(strings.ToLower(ext), ".")
	if ext == "" {
		return "", fmt.Errorf("storage: ext is required")
	}
	if strings.ContainsAny(ext, `/\.`) || strings.Contains(ext, "..") {
		return "", fmt.Errorf("storage: invalid ext")
	}
	for _, r := range ext {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		return "", fmt.Errorf("storage: invalid ext")
	}
	return ext, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
