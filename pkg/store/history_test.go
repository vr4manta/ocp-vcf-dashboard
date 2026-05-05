package store

import (
	"testing"

	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/vcenter"
)

func TestClusterHistoryAdd(t *testing.T) {
	h := NewClusterHistory(3)
	h.Add(ClusterStatsPoint{Timestamp: 100, CpuDemandMHz: 1000})
	h.Add(ClusterStatsPoint{Timestamp: 200, CpuDemandMHz: 2000})

	all := h.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 points, got %d", len(all))
	}
	if all[0].Timestamp != 100 || all[1].Timestamp != 200 {
		t.Errorf("unexpected order: %v", all)
	}
}

func TestClusterHistoryWraparound(t *testing.T) {
	h := NewClusterHistory(3)
	h.Add(ClusterStatsPoint{Timestamp: 100, CpuDemandMHz: 1000})
	h.Add(ClusterStatsPoint{Timestamp: 200, CpuDemandMHz: 2000})
	h.Add(ClusterStatsPoint{Timestamp: 300, CpuDemandMHz: 3000})
	h.Add(ClusterStatsPoint{Timestamp: 400, CpuDemandMHz: 4000})

	all := h.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 points after wraparound, got %d", len(all))
	}
	if all[0].Timestamp != 200 {
		t.Errorf("oldest point should be 200, got %d", all[0].Timestamp)
	}
	if all[2].Timestamp != 400 {
		t.Errorf("newest point should be 400, got %d", all[2].Timestamp)
	}
}

func TestClusterHistoryRange(t *testing.T) {
	h := NewClusterHistory(10)
	for i := int64(0); i < 5; i++ {
		h.Add(ClusterStatsPoint{Timestamp: i * 100})
	}

	result := h.Range(100, 300)
	if len(result) != 3 {
		t.Fatalf("expected 3 points in range [100,300], got %d", len(result))
	}
	if result[0].Timestamp != 100 || result[2].Timestamp != 300 {
		t.Errorf("unexpected range result: %v", result)
	}
}

func TestClusterHistoryEmpty(t *testing.T) {
	h := NewClusterHistory(5)
	all := h.All()
	if all != nil {
		t.Errorf("expected nil for empty history, got %v", all)
	}
	result := h.Range(0, 1000)
	if result != nil {
		t.Errorf("expected nil for empty range, got %v", result)
	}
}

func TestStoreRecordAndGetClusterHistory(t *testing.T) {
	s := New()

	clusters := []vcenter.ClusterInfo{
		{
			Name:           "Cluster-1",
			CpuDemandMHz:   50000,
			CpuCapacityMHz: 100000,
			MemDemandMB:    200000,
			MemCapacityMB:  500000,
		},
	}
	s.RecordClusterStats("vc1.example.com", clusters)
	s.RecordClusterStats("vc1.example.com", clusters)

	history := s.GetClusterHistory("vc1.example.com", "Cluster-1", 0, 9999999999)
	if len(history) != 2 {
		t.Fatalf("expected 2 history points, got %d", len(history))
	}
	if history[0].CpuDemandMHz != 50000 {
		t.Errorf("expected CpuDemandMHz 50000, got %d", history[0].CpuDemandMHz)
	}
}

func TestStoreGetClusterHistoryNotFound(t *testing.T) {
	s := New()
	history := s.GetClusterHistory("nonexistent", "cluster", 0, 9999999999)
	if history != nil {
		t.Errorf("expected nil for nonexistent cluster, got %v", history)
	}
}
