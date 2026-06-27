package storage

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

func TestAWSPartUploader_PartUploaderContract(t *testing.T) {
	t.Parallel()

	testPartUploaderContract(t, partUploaderTestAdapter{
		abortsOnClose: true,
		new: func(t *testing.T, recorder *partUploaderRecorder) partUploader {
			t.Helper()

			client := newTestS3Client(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.RawQuery == "uploads=":
					recorder.started = true
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(`<InitiateMultipartUploadResult><UploadId>contract-upload-id</UploadId></InitiateMultipartUploadResult>`))
				case r.Method == http.MethodPut:
					recordUploadedPart(t, recorder, w, r)
				case r.Method == http.MethodPost && strings.Contains(r.URL.RawQuery, "uploadId=contract-upload-id"):
					var complete completeMultipartUploadRequest
					if err := xml.NewDecoder(r.Body).Decode(&complete); err != nil {
						t.Fatalf("decode complete upload request: %v", err)
					}
					recorder.completed = true
					if len(complete.Parts) == 2 && (complete.Parts[0].PartNumber != 1 || complete.Parts[1].PartNumber != 2) {
						t.Fatalf("complete upload parts not sorted: %+v", complete.Parts)
					}
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(`<CompleteMultipartUploadResult><Bucket>test-bucket</Bucket><Key>test-object</Key><ETag>"complete-etag"</ETag></CompleteMultipartUploadResult>`))
				case r.Method == http.MethodDelete && strings.Contains(r.URL.RawQuery, "uploadId=contract-upload-id"):
					recorder.aborted = true
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Fatalf("unexpected AWS multipart request: %s %s", r.Method, r.URL.String())
				}
			})

			return &awsPartUploader{client: client, bucketName: testBucketName, objectName: testObjectName}
		},
	})
}

func TestAWSCompressedStoreFileSetsUncompressedSizeMetadataAndSizeUsesIt(t *testing.T) {
	t.Parallel()

	inputPath := writeTempFile(t, []byte(strings.Repeat("compressible-data", 1024)))
	inputSize := int64(len(strings.Repeat("compressible-data", 1024)))
	var metadata map[string]string

	client := newTestS3Client(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.RawQuery == "uploads=":
			metadata = s3MetadataFromHeaders(r.Header)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`<InitiateMultipartUploadResult><UploadId>metadata-upload-id</UploadId></InitiateMultipartUploadResult>`))
		case r.Method == http.MethodPut && strings.Contains(r.URL.RawQuery, "partNumber="):
			w.Header().Set("ETag", `"etag1"`)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.Contains(r.URL.RawQuery, "uploadId=metadata-upload-id"):
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`<CompleteMultipartUploadResult><Bucket>test-bucket</Bucket><Key>test-object</Key><ETag>"complete-etag"</ETag></CompleteMultipartUploadResult>`))
		case r.Method == http.MethodHead:
			w.Header().Set("Content-Length", "17")
			if size := metadata[MetadataKeyUncompressedSize]; size != "" {
				w.Header().Set("x-amz-meta-"+MetadataKeyUncompressedSize, size)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected AWS request: %s %s", r.Method, r.URL.String())
		}
	})

	obj := &awsObject{client: client, bucketName: testBucketName, path: testObjectName}
	_, _, err := obj.StoreFile(t.Context(), inputPath, WithCompressConfig(testCompressConfig()))
	require.NoError(t, err)
	require.Equal(t, strconv.FormatInt(inputSize, 10), metadata[MetadataKeyUncompressedSize])

	size, err := obj.Size(t.Context())
	require.NoError(t, err)
	require.Equal(t, inputSize, size)
}

func TestAWSCompressedStoreFileAbortsMultipartUploadOnFailure(t *testing.T) {
	t.Parallel()

	inputPath := writeTempFile(t, []byte(strings.Repeat("compressible-data", 1024)))
	var aborted atomic.Bool

	client := newTestS3Client(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.RawQuery == "uploads=":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`<InitiateMultipartUploadResult><UploadId>abort-upload-id</UploadId></InitiateMultipartUploadResult>`))
		case r.Method == http.MethodPut && strings.Contains(r.URL.RawQuery, "partNumber="):
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.RawQuery, "uploadId=abort-upload-id"):
			aborted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected AWS request: %s %s", r.Method, r.URL.String())
		}
	})

	obj := &awsObject{client: client, bucketName: testBucketName, path: testObjectName}
	_, _, err := obj.StoreFile(t.Context(), inputPath, WithCompressConfig(testCompressConfig()))
	require.Error(t, err)
	require.True(t, aborted.Load(), "failed compressed upload should abort the multipart upload")
}

type completeMultipartUploadRequest struct {
	Parts []struct {
		PartNumber int `xml:"PartNumber"`
	} `xml:"Part"`
}

func newTestS3Client(t *testing.T, handler http.HandlerFunc) *s3.Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return s3.NewFromConfig(aws.Config{
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		Region:       "us-east-1",
		BaseEndpoint: aws.String(server.URL),
		HTTPClient:   server.Client(),
	}, func(o *s3.Options) {
		o.UsePathStyle = true
	})
}

func writeTempFile(t *testing.T, data []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "input")
	require.NoError(t, os.WriteFile(path, data, 0o644))

	return path
}

func testCompressConfig() CompressConfig {
	return CompressConfig{Enabled: true, Type: CompressionLZ4.String(), FrameSizeKB: 1, MinPartSizeMB: 1, FrameEncodeWorkers: 1}
}

func s3MetadataFromHeaders(h http.Header) map[string]string {
	metadata := make(map[string]string)
	for key, values := range h {
		if strings.HasPrefix(strings.ToLower(key), "x-amz-meta-") && len(values) > 0 {
			metadata[strings.TrimPrefix(strings.ToLower(key), "x-amz-meta-")] = values[0]
		}
	}

	return metadata
}
