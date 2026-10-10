// Package s3store reads templates from AWS S3 or any S3-compatible object
// storage such as MinIO.
package s3store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/theo-mrn/kourrier/internal/storage"
)

// Config configures the S3 client. Endpoint, AccessKeyID and SecretAccessKey
// are only needed for non-AWS services (e.g. MinIO); on AWS the default
// credential chain (IRSA, instance profile, env) is used.
type Config struct {
	Bucket          string
	Prefix          string
	Region          string
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	UsePathStyle    bool
}

// Store reads objects from a bucket, under an optional key prefix.
type Store struct {
	client *s3.Client
	bucket string
	prefix string
}

// New creates a Store.
func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3store: bucket is required")
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if cfg.AccessKeyID != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("s3store: load AWS config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.UsePathStyle
		// Only validate checksums when the server sends them: MinIO and other
		// S3-compatible services don't always do so.
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	return &Store{client: client, bucket: cfg.Bucket, prefix: cfg.Prefix}, nil
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	fullKey := path.Join(s.prefix, key)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("s3store: get %q: %w", fullKey, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, storage.MaxObjectSize+1))
	if err != nil {
		return nil, fmt.Errorf("s3store: read %q: %w", fullKey, err)
	}
	if len(data) > storage.MaxObjectSize {
		return nil, fmt.Errorf("s3store: %q exceeds %d bytes", fullKey, storage.MaxObjectSize)
	}
	return data, nil
}

func (s *Store) Ping(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	if err != nil {
		return fmt.Errorf("s3store: head bucket %q: %w", s.bucket, err)
	}
	return nil
}

func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var re *smithyhttp.ResponseError
	return errors.As(err, &re) && re.HTTPStatusCode() == http.StatusNotFound
}
