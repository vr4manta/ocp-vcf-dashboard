package store

import (
	"time"

	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/vcenter"
)

const defaultHistoryCapacity = 2016 // 7 days at 5-minute intervals

// ClusterStatsPoint represents a single time-series data point for cluster resource usage.
type ClusterStatsPoint struct {
	Timestamp      int64 `json:"ts"`
	CpuDemandMHz   int32 `json:"cpu_demand_mhz"`
	CpuCapacityMHz int32 `json:"cpu_capacity_mhz"`
	MemDemandMB    int32 `json:"mem_demand_mb"`
	MemCapacityMB  int32 `json:"mem_capacity_mb"`
}

// ClusterHistory is a fixed-capacity ring buffer of ClusterStatsPoint entries.
type ClusterHistory struct {
	points []ClusterStatsPoint
	head   int
	size   int
	cap    int
}

// NewClusterHistory creates a ring buffer with the given capacity.
func NewClusterHistory(capacity int) *ClusterHistory {
	return &ClusterHistory{
		points: make([]ClusterStatsPoint, capacity),
		cap:    capacity,
	}
}

// Add appends a data point, overwriting the oldest when full.
func (h *ClusterHistory) Add(p ClusterStatsPoint) {
	h.points[h.head] = p
	h.head = (h.head + 1) % h.cap
	if h.size < h.cap {
		h.size++
	}
}

// Range returns points within [start, end] Unix timestamps in chronological order.
func (h *ClusterHistory) Range(start, end int64) []ClusterStatsPoint {
	all := h.All()
	var result []ClusterStatsPoint
	for _, p := range all {
		if p.Timestamp >= start && p.Timestamp <= end {
			result = append(result, p)
		}
	}
	return result
}

// All returns all stored points in chronological order.
func (h *ClusterHistory) All() []ClusterStatsPoint {
	if h.size == 0 {
		return nil
	}
	result := make([]ClusterStatsPoint, h.size)
	if h.size < h.cap {
		copy(result, h.points[:h.size])
	} else {
		start := h.head % h.cap
		n := copy(result, h.points[start:])
		copy(result[n:], h.points[:start])
	}
	return result
}

// RecordClusterStats appends current cluster stats to the history ring buffers.
func (s *Store) RecordClusterStats(server string, clusters []vcenter.ClusterInfo) {
	now := time.Now().Unix()
	s.mu.Lock()
	for _, c := range clusters {
		key := server + ":" + c.Name
		hist, ok := s.clusterHistory[key]
		if !ok {
			hist = NewClusterHistory(defaultHistoryCapacity)
			s.clusterHistory[key] = hist
		}
		hist.Add(ClusterStatsPoint{
			Timestamp:      now,
			CpuDemandMHz:   c.CpuDemandMHz,
			CpuCapacityMHz: c.CpuCapacityMHz,
			MemDemandMB:    c.MemDemandMB,
			MemCapacityMB:  c.MemCapacityMB,
		})
	}
	s.mu.Unlock()
}

// GetClusterHistory returns cluster stats history within the given time range.
func (s *Store) GetClusterHistory(server, clusterName string, start, end int64) []ClusterStatsPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := server + ":" + clusterName
	hist, ok := s.clusterHistory[key]
	if !ok {
		return nil
	}
	return hist.Range(start, end)
}
