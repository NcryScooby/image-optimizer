package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/notrealscooby/image-optimizer/internal/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// Storage manages original images and AVIF variants in an S3-compatible bucket.
//
// Layout (object keys):
//
//	originals/{id}.{ext}
//	variants/{id}/{params_hash}.avif
type Storage struct {
	client *s3.Client
	bucket string
}

// New creates an S3 Storage client (path-style when configured) and ensures the bucket exists.
func New(ctx context.Context, cfg config.Config) (*Storage, error) {
	if cfg.S3Endpoint == "" || cfg.S3Region == "" || cfg.S3Bucket == "" {
		return nil, fmt.Errorf("storage: S3 endpoint, region, and bucket are required")
	}
	if cfg.S3AccessKey == "" || cfg.S3SecretKey == "" {
		return nil, fmt.Errorf("storage: S3 access key and secret key are required")
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.S3AccessKey,
			cfg.S3SecretKey,
			"",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("storage: load aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.S3Endpoint)
		o.UsePathStyle = cfg.S3UsePathStyle
	})

	s := &Storage{client: client, bucket: cfg.S3Bucket}
	if err := s.ensureBucket(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Bucket returns the configured S3 bucket name.
func (s *Storage) Bucket() string {
	return s.bucket
}

// SaveOriginal writes the original image to originals/{id}.{ext}.
// Returns a relative object key (e.g. "originals/{id}.{ext}").
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

	key := path.Join("originals", id+"."+ext)
	if err := s.putObject(ctx, key, data); err != nil {
		return "", fmt.Errorf("storage: put original: %w", err)
	}
	return key, nil
}

// VariantPath returns the relative key for a variant:
// variants/{imageID}/{paramsHash}.avif
func (s *Storage) VariantPath(imageID, paramsHash string) string {
	return path.Join("variants", imageID, paramsHash+".avif")
}

// WriteVariant writes AVIF bytes to variants/{imageID}/{paramsHash}.avif.
// Returns a relative object key.
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

	key := s.VariantPath(imageID, paramsHash)
	if err := s.putObject(ctx, key, data); err != nil {
		return "", fmt.Errorf("storage: put variant: %w", err)
	}
	return key, nil
}

// DeleteImageFiles removes the original object and all objects under variants/{imageID}/.
// originalPath is a relative object key (e.g. "originals/{id}.ext").
func (s *Storage) DeleteImageFiles(ctx context.Context, imageID, originalPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(imageID); err != nil {
		return err
	}

	var firstErr error

	if originalPath != "" {
		key, err := normalizeKey(originalPath)
		if err != nil {
			firstErr = err
		} else if err := s.deleteObject(ctx, key); err != nil {
			firstErr = fmt.Errorf("storage: delete original: %w", err)
		}
	}

	prefix := path.Join("variants", imageID) + "/"
	if err := s.deletePrefix(ctx, prefix); err != nil {
		if firstErr == nil {
			return fmt.Errorf("storage: delete variants: %w", err)
		}
		return fmt.Errorf("storage: delete variants: %w (also: %v)", err, firstErr)
	}
	return firstErr
}

// Get fetches an object by relative key. Caller must Close the returned body.
// Size is the object ContentLength when known; -1 if unknown.
func (s *Storage) Get(ctx context.Context, relPath string) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	key, err := normalizeKey(relPath)
	if err != nil {
		return nil, 0, err
	}

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("storage: get object %q: %w", key, err)
	}

	size := int64(-1)
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	return out.Body, size, nil
}

func (s *Storage) ensureBucket(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(s.bucket),
	})
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return fmt.Errorf("storage: head bucket %q: %w", s.bucket, err)
	}

	_, err = s.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(s.bucket),
	})
	if err != nil {
		var exists *types.BucketAlreadyOwnedByYou
		var exists2 *types.BucketAlreadyExists
		if errors.As(err, &exists) || errors.As(err, &exists2) {
			return nil
		}
		return fmt.Errorf("storage: create bucket %q: %w", s.bucket, err)
	}
	return nil
}

func (s *Storage) putObject(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	return err
}

func (s *Storage) deleteObject(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

func (s *Storage) deletePrefix(ctx context.Context, prefix string) error {
	var token *string
	for {
		out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return err
		}
		if len(out.Contents) == 0 {
			if out.IsTruncated != nil && *out.IsTruncated && out.NextContinuationToken != nil {
				token = out.NextContinuationToken
				continue
			}
			return nil
		}

		objs := make([]types.ObjectIdentifier, 0, len(out.Contents))
		for _, obj := range out.Contents {
			if obj.Key == nil || *obj.Key == "" {
				continue
			}
			objs = append(objs, types.ObjectIdentifier{Key: obj.Key})
		}
		if len(objs) > 0 {
			_, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(s.bucket),
				Delete: &types.Delete{
					Objects: objs,
					Quiet:   aws.Bool(true),
				},
			})
			if err != nil {
				return err
			}
		}

		if out.IsTruncated == nil || !*out.IsTruncated {
			return nil
		}
		token = out.NextContinuationToken
	}
}

func normalizeKey(relPath string) (string, error) {
	key := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(relPath, "\\", "/")), "/")
	if key == "" || key == "." {
		return "", fmt.Errorf("storage: empty path")
	}
	if strings.HasPrefix(key, "..") || strings.Contains(key, "/../") {
		return "", fmt.Errorf("storage: path escapes bucket")
	}
	return key, nil
}

func isNotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchBucket", "404":
			return true
		}
	}
	return false
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
