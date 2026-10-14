package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	vcmv1 "github.com/openshift-splat-team/vsphere-capacity-manager/pkg/apis/vspherecapacitymanager.splat.io/v1"
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
	// Align the window to step boundaries so Prometheus evaluates at round
	// instants; every range then samples the same wall-clock times.
	start, now = alignWindow(start, now, step)

	resp := GraphResponse{
		PoolName:    poolName,
		ClusterName: clusterName,
		Server:      server,
		Range:       rangeParam,
	}

	var promCPU, promMem []tsVal
	var clusterCPU, clusterMem []tsVal

	if s.promClient != nil {
		promCPU, promMem = s.queryPoolMetrics(r.Context(), poolName, start, now, step)
	}

	if clusterName != "" && s.metadata != nil {
		clusterCPU, clusterMem = s.queryClusterPerfMetrics(r.Context(), server, clusterName, start, now, rangeParam)
	}

	stepSec := int64(step.Seconds())
	resp.CPU = buildSeries(start.Unix(), now.Unix(), stepSec, promCPU, clusterCPU)
	resp.Memory = buildSeries(start.Unix(), now.Unix(), stepSec, promMem, clusterMem)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		klog.Errorf("encoding graph response: %v", err)
	}
}

// NetworkGraphResponse is the JSON response for per-pool network usage graph data.
type NetworkGraphResponse struct {
	Range      string              `json:"range"`
	Timestamps []int64             `json:"timestamps"`
	Pools      []PoolNetworkSeries `json:"pools"`
}

// PoolNetworkSeries holds one pool's single/multi-tenant usage series, aligned
// to the response's shared Timestamps grid. Null entries mark grid points
// without data (before the series starts, during gaps, and after it ends).
type PoolNetworkSeries struct {
	PoolName        string     `json:"pool_name"`
	SingleTenantPct []*float64 `json:"single_tenant_pct"`
	MultiTenantPct  []*float64 `json:"multi_tenant_pct"`
}

// handleNetworkGraph serves per-pool network usage history computed from the
// metrics the vsphere-capacity-manager publishes to Prometheus
// (pool_networks_total_by_type / pool_networks_available_by_type).
func (s *Server) handleNetworkGraph(w http.ResponseWriter, r *http.Request) {
	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}

	duration, step, err := parseRange(rangeParam)
	if err != nil {
		http.Error(w, "invalid range parameter", http.StatusBadRequest)
		return
	}

	now := time.Now()
	start := now.Add(-duration)
	// Align the window to step boundaries so Prometheus evaluates at round
	// instants; every range then samples the same wall-clock times (e.g.
	// 09:00:00 reads identically on the 1h and the 7d chart).
	start, now = alignWindow(start, now, step)
	stepSec := int64(step.Seconds())

	var stPoints, mtPoints map[string][]tsVal
	if s.promClient != nil {
		stPoints, mtPoints = s.queryNetworkUsageMetrics(r.Context(), start, now, step)
	}

	resp := buildNetworkGraphResponse(rangeParam, start.Unix(), now.Unix(), stepSec, stPoints, mtPoints)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		klog.Errorf("encoding network graph response: %v", err)
	}
}

// buildNetworkGraphResponse assembles the shared network graph JSON response
// from per-pool single/multi-tenant point series, aligned onto a regular grid.
func buildNetworkGraphResponse(rangeParam string, startUnix, endUnix, stepSec int64, stPoints, mtPoints map[string][]tsVal) NetworkGraphResponse {
	resp := NetworkGraphResponse{Range: rangeParam}

	poolNames := make(map[string]struct{}, len(stPoints)+len(mtPoints))
	for name := range stPoints {
		poolNames[name] = struct{}{}
	}
	for name := range mtPoints {
		poolNames[name] = struct{}{}
	}
	names := make([]string, 0, len(poolNames))
	for name := range poolNames {
		names = append(names, name)
	}
	sort.Strings(names)

	timestamps := buildGrid(startUnix, endUnix, stepSec)
	if timestamps != nil {
		resp.Timestamps = timestamps
		tolerance := stepSec / 2
		if tolerance < 1 {
			tolerance = 1
		}
		for _, name := range names {
			series := PoolNetworkSeries{PoolName: name}
			if pts := stPoints[name]; len(pts) > 0 {
				series.SingleTenantPct = snapToGridNullable(timestamps, pts, tolerance)
			}
			if pts := mtPoints[name]; len(pts) > 0 {
				series.MultiTenantPct = snapToGridNullable(timestamps, pts, tolerance)
			}
			resp.Pools = append(resp.Pools, series)
		}
	}

	return resp
}

type tsVal struct {
	ts  int64
	val float64
}

// buildGrid generates a regular timestamp grid aligned to step, or nil if the
// resulting range would be empty.
func buildGrid(startUnix, endUnix, stepSec int64) []int64 {
	gridStart := (startUnix / stepSec) * stepSec
	if gridStart < startUnix {
		gridStart += stepSec
	}

	n := int((endUnix-gridStart)/stepSec) + 1
	if n <= 0 {
		return nil
	}

	timestamps := make([]int64, n)
	for i := range timestamps {
		timestamps[i] = gridStart + int64(i)*stepSec
	}
	return timestamps
}

// buildSeries generates a regular timestamp grid and snaps both data sources
// onto it using nearest-neighbor matching (within step/2 tolerance).
func buildSeries(startUnix, endUnix, stepSec int64, pool, cluster []tsVal) GraphSeries {
	if len(pool) == 0 && len(cluster) == 0 {
		return GraphSeries{}
	}

	timestamps := buildGrid(startUnix, endUnix, stepSec)
	if timestamps == nil {
		return GraphSeries{}
	}

	tolerance := stepSec / 2
	if tolerance < 1 {
		tolerance = 1
	}

	series := GraphSeries{
		Timestamps: timestamps,
	}

	if len(pool) > 0 {
		series.PoolUsedPct = snapToGrid(timestamps, pool, tolerance)
	}
	if len(cluster) > 0 {
		series.ClusterDemandPct = snapToGrid(timestamps, cluster, tolerance)
	}

	return series
}

// advanceToNearest moves the source index forward to the point closest to gt.
func advanceToNearest(src []tsVal, srcIdx int, gt int64) int {
	for srcIdx < len(src)-1 {
		if abs64(src[srcIdx+1].ts-gt) > abs64(src[srcIdx].ts-gt) {
			break
		}
		srcIdx++
	}
	return srcIdx
}

// snapToGridNullable maps source points onto a regular grid, emitting null
// wherever no source point falls within tolerance of a grid timestamp: before
// the series starts, during gaps, and after it ends. Missing data must render
// as a gap rather than a zero or a carried-forward flat line, both of which
// read as real usage values.
func snapToGridNullable(grid []int64, src []tsVal, tolerance int64) []*float64 {
	out := make([]*float64, len(grid))
	srcIdx := 0

	for i, gt := range grid {
		srcIdx = advanceToNearest(src, srcIdx, gt)
		if srcIdx < len(src) && abs64(src[srcIdx].ts-gt) <= tolerance {
			v := src[srcIdx].val
			out[i] = &v
		}
	}

	return out
}

// snapToGrid maps irregular source data onto a regular grid using nearest-neighbor.
// For each grid timestamp, it finds the nearest source point within tolerance.
// Points outside tolerance get the last known value (step interpolation).
func snapToGrid(grid []int64, src []tsVal, tolerance int64) []float64 {
	out := make([]float64, len(grid))
	srcIdx := 0
	lastVal := math.NaN()

	for i, gt := range grid {
		// Advance source index to the closest point to this grid time
		srcIdx = advanceToNearest(src, srcIdx, gt)

		if srcIdx < len(src) && abs64(src[srcIdx].ts-gt) <= tolerance {
			lastVal = src[srcIdx].val
		}

		if !math.IsNaN(lastVal) {
			out[i] = lastVal
		}
	}

	return out
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
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

// queryNetworkUsageMetrics fetches per-pool single/multi-tenant network usage
// percentages from the metrics the vsphere-capacity-manager publishes to
// Prometheus (pool_networks_total_by_type / pool_networks_available_by_type).
func (s *Server) queryNetworkUsageMetrics(ctx context.Context, start, end time.Time, step time.Duration) (stPoints, mtPoints map[string][]tsVal) {
	stPoints = make(map[string][]tsVal)
	mtPoints = make(map[string][]tsVal)

	ns := "vsphere-infra-helpers"
	labels := fmt.Sprintf(`namespace="%s"`, ns)
	// Aggregate away instance/pod labels: a VCM pod replacement starts a new
	// series (new pod name and IP), and without this the dead pod's ended
	// series and the live one would both come back for long ranges. max by
	// stitches them into one series per pool and network type, taking the
	// only (live) series that has samples at each evaluation instant.
	total := fmt.Sprintf(`max by (namespace, pool, networkType) (pool_networks_total_by_type{%s})`, labels)
	avail := fmt.Sprintf(`max by (namespace, pool, networkType) (pool_networks_available_by_type{%s})`, labels)
	query := fmt.Sprintf(`100 * (%s - %s) / %s`, total, avail, total)

	series, err := s.promClient.QueryRangeSeries(ctx, query, start, end, step)
	if err != nil {
		klog.V(2).Infof("Prometheus query error for network usage: %v", err)
		return
	}

	// Skip pools excluded from scheduling, mirroring the in-pod sampler which
	// records history only for pools without spec.exclude.
	excluded := make(map[string]struct{})
	for _, p := range s.store.GetPools() {
		if p.Spec.Exclude {
			excluded[p.Name] = struct{}{}
		}
	}

	for _, result := range series {
		pool := result.Labels["pool"]
		if pool == "" {
			continue
		}
		if _, skip := excluded[pool]; skip {
			continue
		}

		points := make([]tsVal, 0, len(result.Points))
		for _, p := range result.Points {
			if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
				continue
			}
			points = append(points, tsVal{ts: p.Timestamp, val: p.Value})
		}
		if len(points) == 0 {
			continue
		}

		switch result.Labels["networkType"] {
		case string(vcmv1.NetworkTypeSingleTenant):
			stPoints[pool] = points
		case string(vcmv1.NetworkTypeMultiTenant):
			mtPoints[pool] = points
		}
	}

	return stPoints, mtPoints
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

// alignWindow snaps the query window onto step boundaries (floor). Prometheus
// evaluates a range query at start + k*step, so an aligned window makes every
// chart range evaluate the same wall-clock instants, and the returned
// evaluation timestamps coincide exactly with the chart's display grid.
func alignWindow(start, end time.Time, step time.Duration) (time.Time, time.Time) {
	stepSec := int64(step.Seconds())
	alignedStart := (start.Unix() / stepSec) * stepSec
	alignedEnd := (end.Unix() / stepSec) * stepSec
	return time.Unix(alignedStart, 0), time.Unix(alignedEnd, 0)
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
