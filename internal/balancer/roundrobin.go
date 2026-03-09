package balancer

import (
	"sync/atomic"

	"intelligent-lb/internal/metrics"
)

// RoundRobin distributes requests evenly in sequence: S1→S2→S3→S1→...
// Used as fallback at startup before latency metrics are collected.
type RoundRobin struct {
	counter atomic.Uint64
}

// Select picks the next server in round-robin order.
func (rr *RoundRobin) Select(
	candidates []string,
	stats map[string]metrics.ServerStats,
	priority string,
) string {
	if len(candidates) == 0 {
		return ""
	}
	idx := rr.counter.Add(1) - 1
	return candidates[idx%uint64(len(candidates))]
}
