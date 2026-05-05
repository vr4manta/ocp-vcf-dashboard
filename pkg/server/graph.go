package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// GraphResponse is the JSON response for pool graph data.
type GraphResponse struct {
	PoolName    string      `json:"pool_name"`
	ClusterName string      `json:"cluster_name"`
	Server      string      `json:"server"`
	Range       string      `json:"range"`
	CPU         GraphSeries `json:"cpu"`
	Memory      GraphSeries `json:"memory"`
}

// GraphSeries holds aligned time series data for a resource type.
type GraphSeries struct {
	Timestamps       []int64   `json:"timestamps"`
	PoolUsedPct      []float64 `json:"pool_used_pct"`
	ClusterDemandPct []float64 `json:"cluster_demand_pct"`
}

func (s *Server) handlePoolGraph(w http.ResponseWriter, r *http.Request) {
	poolName := strings.TrimPrefix(r.URL.Path, "/api/graph/pool/")
	if poolName == "" {
		http.NotFound(w, r)
		return
	}

	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}

	duration, step, err := parseRange(rangeParam)
	if err != nil {
		http.Error(w, "invalid range parameter", http.StatusBadRequest)
		return
	}

	pool := s.store.GetPool(poolName)
	if pool == nil {
		http.NotFound(w, r)
		return
	}

	clusterName := extractClusterName(pool.Spec.Topology.ComputeCluster)
	server := pool.Spec.Server

	now := time.Now()
	start := now.Add(-duration)

	resp := GraphResponse{
		PoolName:    poolName,
		ClusterName: clusterName,
		Server:      server,
		Range:       rangeParam,
	}

	// Query Prometheus for pool metrics
	if s.promClient != nil {
		s.queryPoolMetrics(r.Context(), poolName, start, now, step, &resp)
	}

	// Query local cluster history
	if clusterName != "" {
		history := s.store.GetClusterHistory(server, clusterName, start.Unix(), now.Unix())
		for _, p := range history {
			resp.CPU.Timestamps = append(resp.CPU.Timestamps, p.Timestamp)
			if p.CpuCapacityMHz > 0 {
				resp.CPU.ClusterDemandPct = append(resp.CPU.ClusterDemandPct,
					float64(p.CpuDemandMHz)/float64(p.CpuCapacityMHz)*100)
			} else {
				resp.CPU.ClusterDemandPct = append(resp.CPU.ClusterDemandPct, 0)
			}

			resp.Memory.Timestamps = append(resp.Memory.Timestamps, p.Timestamp)
			if p.MemCapacityMB > 0 {
				resp.Memory.ClusterDemandPct = append(resp.Memory.ClusterDemandPct,
					float64(p.MemDemandMB)/float64(p.MemCapacityMB)*100)
			} else {
				resp.Memory.ClusterDemandPct = append(resp.Memory.ClusterDemandPct, 0)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		klog.Errorf("encoding graph response: %v", err)
	}
}

func (s *Server) queryPoolMetrics(ctx context.Context, poolName string, start, end time.Time, step time.Duration, resp *GraphResponse) {
	ns := "vsphere-infra-helpers"
	labels := fmt.Sprintf(`namespace="%s",pool="%s"`, ns, poolName)

	cpuTotalQuery := fmt.Sprintf(`pool_cpus_total{%s}`, labels)
	cpuAvailQuery := fmt.Sprintf(`pool_cpus_available{%s}`, labels)
	memTotalQuery := fmt.Sprintf(`pool_memory_total{%s}`, labels)
	memAvailQuery := fmt.Sprintf(`pool_memory_available{%s}`, labels)

	cpuTotal, err := s.promClient.QueryRange(ctx, cpuTotalQuery, start, end, step)
	if err != nil {
		klog.V(2).Infof("Prometheus query error for pool %s CPU total: %v", poolName, err)
		return
	}
	cpuAvail, err := s.promClient.QueryRange(ctx, cpuAvailQuery, start, end, step)
	if err != nil {
		klog.V(2).Infof("Prometheus query error for pool %s CPU avail: %v", poolName, err)
		return
	}

	if len(cpuTotal) > 0 && len(cpuTotal) == len(cpuAvail) {
		for i := range cpuTotal {
			if resp.CPU.Timestamps == nil {
				resp.CPU.Timestamps = make([]int64, 0, len(cpuTotal))
			}
			total := cpuTotal[i].Value
			used := total - cpuAvail[i].Value
			pct := 0.0
			if total > 0 {
				pct = used / total * 100
			}
			resp.CPU.PoolUsedPct = append(resp.CPU.PoolUsedPct, pct)
		}
	}

	memTotal, err := s.promClient.QueryRange(ctx, memTotalQuery, start, end, step)
	if err != nil {
		klog.V(2).Infof("Prometheus query error for pool %s mem total: %v", poolName, err)
		return
	}
	memAvail, err := s.promClient.QueryRange(ctx, memAvailQuery, start, end, step)
	if err != nil {
		klog.V(2).Infof("Prometheus query error for pool %s mem avail: %v", poolName, err)
		return
	}

	if len(memTotal) > 0 && len(memTotal) == len(memAvail) {
		for i := range memTotal {
			if resp.Memory.Timestamps == nil {
				resp.Memory.Timestamps = make([]int64, 0, len(memTotal))
			}
			total := memTotal[i].Value
			used := total - memAvail[i].Value
			pct := 0.0
			if total > 0 {
				pct = used / total * 100
			}
			resp.Memory.PoolUsedPct = append(resp.Memory.PoolUsedPct, pct)
		}
	}
}

func parseRange(r string) (duration time.Duration, step time.Duration, err error) {
	switch r {
	case "1h":
		return time.Hour, 60 * time.Second, nil
	case "6h":
		return 6 * time.Hour, 5 * time.Minute, nil
	case "24h":
		return 24 * time.Hour, 10 * time.Minute, nil
	case "7d":
		return 7 * 24 * time.Hour, 30 * time.Minute, nil
	default:
		return 0, 0, fmt.Errorf("unsupported range: %s", r)
	}
}

// extractClusterName parses a vSphere compute cluster path and returns the cluster name.
// Path format: /<datacenter>/host/<clustername> or /<datacenter>/host/<clustername>/Resources/...
func extractClusterName(computeCluster string) string {
	parts := strings.Split(computeCluster, "/")
	for i, p := range parts {
		if p == "host" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}
