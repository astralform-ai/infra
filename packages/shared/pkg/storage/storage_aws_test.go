package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAWSDeleteObjectsWithPrefixRejectsEmptyPrefix(t *testing.T) {
	t.Parallel()

	s := &awsStorage{bucketName: "test-bucket"}

	err := s.DeleteObjectsWithPrefix(context.Background(), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty prefix")
}
