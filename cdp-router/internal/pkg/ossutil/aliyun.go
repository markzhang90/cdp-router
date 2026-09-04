package ossutil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

type Store struct {
	bucketName string
	bucket     *oss.Bucket
}

func New(cfg config.OSSConfig) (*Store, error) {
	endpoint := endpointFromConfig(cfg)
	if strings.Contains(endpoint, "internal") {
		return nil, fmt.Errorf("oss endpoint must be public when issuing supplier upload urls: %s", endpoint)
	}
	options := []oss.ClientOption{}
	if cfg.Region != "" {
		options = append(options, oss.Region(cfg.Region), oss.AuthVersion(oss.AuthV4))
	}
	client, err := oss.New(endpoint, cfg.AccessKeyID, cfg.AccessKeySecret, options...)
	if err != nil {
		return nil, fmt.Errorf("create oss client: %w", err)
	}
	bucket, err := client.Bucket(cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("open oss bucket: %w", err)
	}
	return &Store{bucketName: cfg.Bucket, bucket: bucket}, nil
}

func endpointFromConfig(cfg config.OSSConfig) string {
	switch {
	case cfg.Endpoint != "":
		return normalizeEndpoint(cfg.Endpoint)
	case cfg.Region != "":
		return "https://oss-" + cfg.Region + ".aliyuncs.com"
	default:
		return normalizeEndpoint(cfg.Host)
	}
}

func normalizeEndpoint(endpoint string) string {
	if endpoint == "" {
		return endpoint
	}
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return strings.TrimRight(endpoint, "/")
	}
	return "https://" + strings.TrimRight(endpoint, "/")
}

func (s *Store) BucketName() string {
	return s.bucketName
}

func (s *Store) SignedPutURL(ctx context.Context, key string, expires time.Duration) (string, time.Time, error) {
	select {
	case <-ctx.Done():
		return "", time.Time{}, ctx.Err()
	default:
	}
	seconds := int64(expires.Seconds())
	signedURL, err := s.bucket.SignURL(key, oss.HTTPPut, seconds)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := ensurePublicURL(signedURL); err != nil {
		return "", time.Time{}, err
	}
	return signedURL, time.Now().Add(expires), nil
}

func (s *Store) PutTemporaryObject(ctx context.Context, key string, contentType string, content []byte, retentionDays int) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if retentionDays <= 0 {
		retentionDays = 7
	}
	tagging := temporaryObjectTagging(retentionDays)
	options := []oss.Option{oss.SetTagging(tagging)}
	if strings.TrimSpace(contentType) != "" {
		options = append(options, oss.ContentType(contentType))
	}
	return s.bucket.PutObject(key, bytes.NewReader(content), options...)
}

func (s *Store) SignedGetURL(ctx context.Context, key string, expires time.Duration) (string, time.Time, error) {
	select {
	case <-ctx.Done():
		return "", time.Time{}, ctx.Err()
	default:
	}
	seconds := int64(expires.Seconds())
	signedURL, err := s.bucket.SignURL(key, oss.HTTPGet, seconds)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := ensurePublicURL(signedURL); err != nil {
		return "", time.Time{}, err
	}
	return signedURL, time.Now().Add(expires), nil
}

func (s *Store) ObjectExists(ctx context.Context, key string) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}
	return s.bucket.IsObjectExist(key)
}

func temporaryObjectTagging(retentionDays int) oss.Tagging {
	return oss.Tagging{Tags: []oss.Tag{
		{Key: "cdp-temp", Value: "true"},
		{Key: "expire-days", Value: fmt.Sprint(retentionDays)},
	}}
}

func (s *Store) ReadObject(ctx context.Context, key string) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	reader, err := s.bucket.GetObject(key)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func ensurePublicURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if strings.Contains(parsed.Host, "internal") {
		return fmt.Errorf("signed oss url uses internal endpoint: %s", parsed.Host)
	}
	return nil
}

var _ biz.ObjectStore = (*Store)(nil)
