package objectstore

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/makecindy/cindy-server/internal/platform/config"
)

type Store struct {
	client   *minio.Client
	endpoint string
	Public   string
	Private  string
	Plugins  string
	Skills   string
}

func New(ctx context.Context, cfg *config.Config) (*Store, error) {
	client, err := minio.New(cfg.S3Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		Secure: cfg.S3UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create s3 client: %w", err)
	}
	s := &Store{client: client, endpoint: cfg.S3Endpoint, Public: cfg.S3BucketPublic, Private: cfg.S3BucketPrivate, Plugins: cfg.S3BucketPlugins, Skills: cfg.S3BucketSkills}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for _, bucket := range []string{s.Public, s.Private, s.Plugins, s.Skills} {
		exists, err := client.BucketExists(checkCtx, bucket)
		if err != nil {
			return nil, fmt.Errorf("check bucket %s: %w", bucket, err)
		}
		if !exists {
			if err := client.MakeBucket(checkCtx, bucket, minio.MakeBucketOptions{}); err != nil {
				return nil, fmt.Errorf("create bucket %s: %w", bucket, err)
			}
		}
	}
	publicPolicy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, s.Public)
	_ = client.SetBucketPolicy(checkCtx, s.Public, publicPolicy)
	return s, nil
}

func (s *Store) Client() *minio.Client { return s.client }

func (s *Store) PresignPut(ctx context.Context, bucket, key string, ttl time.Duration, contentType string) (string, error) {
	params := url.Values{}
	if contentType != "" {
		params.Set("Content-Type", contentType)
	}
	u, err := s.client.PresignedPutObject(ctx, bucket, key, ttl)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (s *Store) PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	params := url.Values{}
	u, err := s.client.PresignedGetObject(ctx, bucket, key, ttl, params)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (s *Store) Stat(ctx context.Context, bucket, key string) (minio.ObjectInfo, error) {
	return s.client.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
}

func (s *Store) Remove(ctx context.Context, bucket, key string) error {
	return s.client.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
}

func (s *Store) PublicURL(key string) string {
	return fmt.Sprintf("%s/%s/%s", s.endpoint, s.Public, key)
}
