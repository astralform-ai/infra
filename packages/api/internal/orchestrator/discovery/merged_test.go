package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staticDiscovery is a Discovery stub returning canned nodes or an error.
type staticDiscovery struct {
	nodes []Node
	err   error
}

func (d *staticDiscovery) ListNodes(context.Context) ([]Node, error) {
	if d.err != nil {
		return nil, d.err
	}

	return d.nodes, nil
}

// TestMergedDiscovery_DisjointUnion verifies that nodes known only to one
// backend are all present in the merged result.
func TestMergedDiscovery_DisjointUnion(t *testing.T) {
	t.Parallel()

	primary := &staticDiscovery{nodes: []Node{
		{ShortID: "aaaaaaaa", IPAddress: "10.0.0.1", OrchestratorAddress: "10.0.0.1:5008"},
	}}
	fallback := &staticDiscovery{nodes: []Node{
		{ShortID: "bbbbbbbb", IPAddress: "10.0.0.2", OrchestratorAddress: "10.0.0.2:5008"},
	}}

	d := NewMerged(primary, fallback)
	nodes, err := d.ListNodes(t.Context())
	require.NoError(t, err)
	require.Len(t, nodes, 2)

	shortIDs := []string{nodes[0].ShortID, nodes[1].ShortID}
	assert.ElementsMatch(t, []string{"aaaaaaaa", "bbbbbbbb"}, shortIDs)
}

// TestMergedDiscovery_PrimaryWinsOnConflict verifies dedup by ShortID with the
// primary's entry winning: the service registration carries the orchestrator's
// real bound port, while the node-listing fallback assumes the well-known one.
func TestMergedDiscovery_PrimaryWinsOnConflict(t *testing.T) {
	t.Parallel()

	primary := &staticDiscovery{nodes: []Node{
		{ShortID: "aaaaaaaa", IPAddress: "10.0.0.1", OrchestratorAddress: "10.0.0.1:6123"},
	}}
	fallback := &staticDiscovery{nodes: []Node{
		{ShortID: "aaaaaaaa", IPAddress: "10.0.0.1", OrchestratorAddress: "10.0.0.1:5008"},
	}}

	d := NewMerged(primary, fallback)
	nodes, err := d.ListNodes(t.Context())
	require.NoError(t, err)
	require.Len(t, nodes, 1)

	assert.Equal(t, "aaaaaaaa", nodes[0].ShortID)
	assert.Equal(t, "10.0.0.1:6123", nodes[0].OrchestratorAddress, "primary (service-based) entry must win on conflict")
}

// TestMergedDiscovery_PrimaryError verifies that a primary failure fails the
// whole listing (no silent degradation to fallback-only results).
func TestMergedDiscovery_PrimaryError(t *testing.T) {
	t.Parallel()

	primaryErr := errors.New("nomad agent unreachable")
	primary := &staticDiscovery{err: primaryErr}
	fallback := &staticDiscovery{nodes: []Node{
		{ShortID: "bbbbbbbb", IPAddress: "10.0.0.2", OrchestratorAddress: "10.0.0.2:5008"},
	}}

	d := NewMerged(primary, fallback)
	nodes, err := d.ListNodes(t.Context())
	require.ErrorIs(t, err, primaryErr)
	assert.Nil(t, nodes)
}

// TestMergedDiscovery_FallbackError verifies that a fallback failure also
// fails the whole listing: both backends hit the same local Nomad agent, so a
// partial result is more likely a bug than a real partial outage, and the
// sync loop treats a discovery error as "skip this cycle".
func TestMergedDiscovery_FallbackError(t *testing.T) {
	t.Parallel()

	fallbackErr := errors.New("nomad agent unreachable")
	primary := &staticDiscovery{nodes: []Node{
		{ShortID: "aaaaaaaa", IPAddress: "10.0.0.1", OrchestratorAddress: "10.0.0.1:5008"},
	}}
	fallback := &staticDiscovery{err: fallbackErr}

	d := NewMerged(primary, fallback)
	nodes, err := d.ListNodes(t.Context())
	require.ErrorIs(t, err, fallbackErr)
	assert.Nil(t, nodes)
}

// TestMergedDiscovery_BothEmpty verifies the union of two empty results is an
// empty, non-error result.
func TestMergedDiscovery_BothEmpty(t *testing.T) {
	t.Parallel()

	d := NewMerged(&staticDiscovery{}, &staticDiscovery{})
	nodes, err := d.ListNodes(t.Context())
	require.NoError(t, err)
	assert.Empty(t, nodes)
}
