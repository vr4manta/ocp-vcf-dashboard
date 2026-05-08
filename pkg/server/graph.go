package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/vmware/govmomi/vim25/types"
	"k8s.io/klog/v2"

	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/vcenter"
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
// Nil float64 pointers encode as JSON null so uPlot renders gaps.
type GraphSeries struct {
	Timestamps       []int64    `json:"timestamps"`
	PoolUsedPct      []*float64 `json:"pool_used_pct"`
	ClusterDemandPct []*float64 `json:"cluster_demand_pct"`
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

	var promCPU, promMem []tsVal
	var clusterCPU, clusterMem []tsVal

	// Query Prometheus for pool metrics
	if s.promClient != nil {
		promCPU, promMem = s.queryPoolMetrics(r.Context(), poolName, start, now, step)
	}

	// Query vSphere historical performance for cluster demand
	if clusterName != "" && s.metadata != nil {
		clusterCPU, clusterMem = s.queryClusterPerfMetrics(r.Context(), server, clusterName, start, now, rangeParam)
	}

	// Build aligned timestamp grids for CPU and Memory
	resp.CPU = alignSeries(promCPU, clusterCPU)
	resp.Memory = alignSeries(promMem, clusterMem)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		klog.Errorf("encoding graph response: %v", err)
	}
}

// alignSeries merges two independent time-series onto a shared timestamp grid.
func alignSeries(pool, cluster []tsVal) GraphSeries {
	tsSet := make(map[int64]struct{})
	for _, p := range pool {
		tsSet[p.ts] = struct{}{}
	}
	for _, p := range cluster {
		tsSet[p.ts] = struct{}{}
	}

	if len(tsSet) == 0 {
		return GraphSeries{}
	}

	timestamps := make([]int64, 0, len(tsSet))
	for ts := range tsSet {
		timestamps = append(timestamps, ts)
	}
	sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })

	poolMap := make(map[int64]float64, len(pool))
	for _, p := range pool {
		poolMap[p.ts] = p.val
	}
	clusterMap := make(map[int64]float64, len(cluster))
	for _, p := range cluster {
		clusterMap[p.ts] = p.val
	}

	series := GraphSeries{
		Timestamps:       timestamps,
		PoolUsedPct:      make([]*float64, len(timestamps)),
		ClusterDemandPct: make([]*float64, len(timestamps)),
	}

	for i, ts := range timestamps {
		if v, ok := poolMap[ts]; ok {
			val := v
			series.PoolUsedPct[i] = &val
		}
		if v, ok := clusterMap[ts]; ok {
			val := v
			series.ClusterDemandPct[i] = &val
		}
	}

	return series
}

type tsVal struct {
	ts  int64
	val float64
}

// queryPoolMetrics fetches pool allocation percentages from Prometheus.
func (s *Server) queryPoolMetrics(ctx context.Context, poolName string, start, end time.Time, step time.Duration) (cpuPoints, memPoints []tsVal) {
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
		cpuPoints = make([]tsVal, 0, len(cpuTotal))
		for i := range cpuTotal {
			total := cpuTotal[i].Value
			used := total - cpuAvail[i].Value
			pct := 0.0
			if total > 0 {
				pct = used / total * 100
			}
			cpuPoints = append(cpuPoints, tsVal{ts: cpuTotal[i].Timestamp, val: pct})
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
		memPoints = make([]tsVal, 0, len(memTotal))
		for i := range memTotal {
			total := memTotal[i].Value
			used := total - memAvail[i].Value
			pct := 0.0
			if total > 0 {
				pct = used / total * 100
			}
			memPoints = append(memPoints, tsVal{ts: memTotal[i].Timestamp, val: pct})
		}
	}

	return cpuPoints, memPoints
}

// queryClusterPerfMetrics fetches cluster demand percentages from vSphere QueryPerf.
func (s *Server) queryClusterPerfMetrics(ctx context.Context, server, clusterName string, start, end time.Time, rangeParam string) (cpuPoints, memPoints []tsVal) {
	vcData, _ := s.store.GetVCenterData()
	vcd, ok := vcData[server]
	if !ok {
		return
	}

	var clusterInfo *vcenter.ClusterInfo
	for i := range vcd.Clusters {
		if vcd.Clusters[i].Name == clusterName {
			clusterInfo = &vcd.Clusters[i]
			break
		}
	}
	if clusterInfo == nil || clusterInfo.MoRef == "" {
		return
	}

	pc, err := s.getPerfClient(ctx, server)
	if err != nil {
		klog.V(2).Infof("PerfClient error for %s: %v", server, err)
		return
	}

	clusterMOR := types.ManagedObjectReference{
		Type:  clusterInfo.MoRefType,
		Value: clusterInfo.MoRef,
	}
	intervalID := vcenter.IntervalForRange(rangeParam)

	result, err := pc.QueryClusterPerf(ctx, clusterMOR, start, end, intervalID)
	if err != nil {
		klog.V(2).Infof("QueryClusterPerf error for %s/%s: %v", server, clusterName, err)
		s.evictPerfClient(server)
		return
	}

	cpuCapacity := float64(clusterInfo.CpuCapacityMHz)
	memCapacityKB := float64(clusterInfo.MemCapacityMB) * 1024

	for i, ts := range result.Timestamps {
		if i < len(result.CpuUsageMHz) && cpuCapacity > 0 {
			pct := float64(result.CpuUsageMHz[i]) / cpuCapacity * 100
			cpuPoints = append(cpuPoints, tsVal{ts: ts, val: pct})
		}
		if i < len(result.MemConsumedKB) && memCapacityKB > 0 {
			pct := float64(result.MemConsumedKB[i]) / memCapacityKB * 100
			memPoints = append(memPoints, tsVal{ts: ts, val: pct})
		}
	}

	return cpuPoints, memPoints
}

func (s *Server) getPerfClient(ctx context.Context, server string) (*vcenter.PerfClient, error) {
	s.perfMu.Lock()
	defer s.perfMu.Unlock()

	if pc, ok := s.perfClients[server]; ok {
		return pc, nil
	}

	sess, err := s.metadata.Session(ctx, server)
	if err != nil {
		return nil, fmt.Errorf("session for %s: %w", server, err)
	}

	pc := vcenter.NewPerfClient(sess.Client.Client)
	s.perfClients[server] = pc
	return pc, nil
}

func (s *Server) evictPerfClient(server string) {
	s.perfMu.Lock()
	delete(s.perfClients, server)
	s.perfMu.Unlock()
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
func extractClusterName(computeCluster string) string {
	parts := strings.Split(computeCluster, "/")
	for i, p := range parts {
		if p == "host" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}
