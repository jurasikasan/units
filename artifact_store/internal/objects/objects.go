// Package objects is the store's way to MinIO (any S3), where the bytes live.
package objects

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// NewClient creates a MinIO client from MINIO_ENDPOINT, MINIO_ACCESS_KEY_ID,
// MINIO_SECRET_ACCESS_KEY and MINIO_USE_SSL.
func NewClient(ctx context.Context) *minio.Client {
	endpoint := os.Getenv("MINIO_ENDPOINT")
	if endpoint == "" {
		endpoint = "minio:9000"
	}

	accessKeyID := os.Getenv("MINIO_ACCESS_KEY_ID")
	secretAccessKey := os.Getenv("MINIO_SECRET_ACCESS_KEY")
	useSSL := os.Getenv("MINIO_USE_SSL") == "true"

	// Initialize minio client object.
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		log.Fatalln("Failed to create MinIO client:", err)
	}
	return minioClient
}

// EnsureBucket makes sure bucketName exists.
func EnsureBucket(ctx context.Context, minioClient *minio.Client, bucketName string) error {
	return ensureBucketExists(ctx, minioClient, bucketName)
}

// ensureBucketExists makes sure bucketName exists, creating it if necessary.
// A MakeBucket failure is only treated as an error if the bucket still
// doesn't exist afterwards - this covers the race where two concurrent
// callers both observe the bucket as missing and one loses the create.
func ensureBucketExists(ctx context.Context, minioClient *minio.Client, bucketName string) error {
	exists, err := minioClient.BucketExists(ctx, bucketName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	err = minioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{Region: "us-east-1"})
	if err != nil {
		exists, errBucketExists := minioClient.BucketExists(ctx, bucketName)
		if errBucketExists == nil && exists {
			log.Printf("We already own %s\n", bucketName)
			return nil
		}
		log.Printf("Error creating bucket %s: %v\n", bucketName, err)
		return err
	}

	log.Printf("Successfully created %s\n", bucketName)
	return nil
}

// ErrNotFound is returned (wrapped) by Download when MinIO
// answers that the requested object or bucket does not exist. Callers can
// distinguish it from a MinIO outage or a read failure with errors.Is.
var ErrNotFound = errors.New("minio: object not found")

// isObjectNotFound reports whether err is a MinIO "NoSuchKey"/"NoSuchBucket"
// response, as opposed to any other failure (connection error, 5xx, ...).
func isObjectNotFound(err error) bool {
	if errors.Is(err, ErrNotFound) {
		return true
	}
	er := minio.ToErrorResponse(err)
	return er.Code == "NoSuchKey" || er.Code == "NoSuchBucket"
}

// Download downloads an object and returns its content. It reports the
// failure mode instead of collapsing everything into nil: a missing object (or
// bucket) comes back as a wrapped ErrNotFound, any other failure (MinIO
// unreachable, read error) as a different error. The context governs the MinIO
// request, so cancelling it aborts the download.
func Download(ctx context.Context, minioClient *minio.Client, bucketName, objectName string) ([]byte, error) {
	if minioClient == nil {
		return nil, fmt.Errorf("minio: client is nil when downloading %s/%s", bucketName, objectName)
	}

	reader, err := minioClient.GetObject(ctx, bucketName, objectName, minio.GetObjectOptions{})
	if err != nil {
		if isObjectNotFound(err) {
			return nil, fmt.Errorf("minio: download %s/%s: %w", bucketName, objectName, ErrNotFound)
		}
		return nil, fmt.Errorf("minio: download %s/%s: %v", bucketName, objectName, err)
	}
	defer func() { _ = reader.Close() }()

	var buffer bytes.Buffer
	if _, err = buffer.ReadFrom(reader); err != nil {
		// minio-go surfaces a 404 on the first read of the object body, so a
		// missing object can fail here rather than from GetObject itself.
		if isObjectNotFound(err) {
			return nil, fmt.Errorf("minio: download %s/%s: %w", bucketName, objectName, ErrNotFound)
		}
		return nil, fmt.Errorf("minio: read %s/%s: %v", bucketName, objectName, err)
	}
	return buffer.Bytes(), nil
}
