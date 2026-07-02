package discovery

import (
	"context"
	"fmt"
)

// mergedDiscovery unions the results of a primary and a fallback Discovery,
// deduplicated by ShortID with the PRIMARY entry winning on conflict. It
// bridges the migration from node-pool-based to service-based discovery: the
// service registration carries the real bound port, so it must win when both
// backends report the same node.
//
// ListNodes fails if EITHER backend fails: failing loudly beats silently
// degrading to a subset of orchestrators, and the caller
// (Orchestrator.syncNodes) treats a discovery error as "skip this cycle,
// keep current state".
type mergedDiscovery struct {
	primary  Discovery
	fallback Discovery
}

// NewMerged creates a Discovery that returns the union of primary's and
// fallback's nodes, deduplicated by ShortID with primary's entries taking
// precedence. See mergedDiscovery for the exact semantics.
func NewMerged(primary, fallback Discovery) Discovery {
	return &mergedDiscovery{
		primary:  primary,
		fallback: fallback,
	}
}

func (d *mergedDiscovery) ListNodes(ctx context.Context) ([]Node, error) {
	ctx, span := tracer.Start(ctx, "list-merged-nodes")
	defer span.End()

	primaryNodes, err := d.primary.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("primary discovery failed: %w", err)
	}

	fallbackNodes, err := d.fallback.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("fallback discovery failed: %w", err)
	}

	out := make([]Node, 0, len(primaryNodes)+len(fallbackNodes))
	seen := make(map[string]struct{}, len(primaryNodes))

	// Backends dedupe their own results, so only cross-backend conflicts
	// need resolving: primary entries are taken as-is and shadow fallback
	// entries with the same ShortID.
	for _, n := range primaryNodes {
		seen[n.ShortID] = struct{}{}
		out = append(out, n)
	}

	for _, n := range fallbackNodes {
		if _, ok := seen[n.ShortID]; ok {
			continue
		}
		out = append(out, n)
	}

	return out, nil
}
