package vcenter

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/methods"
	"github.com/vmware/govmomi/vim25/types"
	"k8s.io/klog/v2"
)

// ClusterPerfResult holds time-series performance data from a QueryPerf call.
type ClusterPerfResult struct {
	Timestamps    []int64
	CpuUsageMHz   []int64
	MemConsumedKB []int64
}

// PerfClient wraps a vim25 client with cached counter resolution for QueryPerf calls.
type PerfClient struct {
	client   *vim25.Client
	counters map[string]int32
	once     sync.Once
	initErr  error
}

// NewPerfClient creates a PerfClient for the given vim25 client.
func NewPerfClient(c *vim25.Client) *PerfClient {
	return &PerfClient{client: c}
}

func (p *PerfClient) resolveCounters(ctx context.Context) error {
	p.once.Do(func() {
		perfMgr := p.client.ServiceContent.PerfManager
		if perfMgr == nil {
			p.initErr = fmt.Errorf("vCenter has no PerformanceManager")
			return
		}

		resp, err := methods.QueryPerfCounterByLevel(ctx, p.client, &types.QueryPerfCounterByLevel{
			This:  *perfMgr,
			Level: 2,
		})
		if err != nil {
			p.initErr = fmt.Errorf("querying perf counters: %w", err)
			return
		}

		p.counters = make(map[string]int32, len(resp.Returnval))
		for _, c := range resp.Returnval {
			group := c.GroupInfo.GetElementDescription().Key
			name := c.NameInfo.GetElementDescription().Key
			rollup := string(c.RollupType)
			key := group + "." + name + "." + rollup
			p.counters[key] = c.Key
		}

		klog.V(2).Infof("Resolved %d perf counters", len(p.counters))
	})
	return p.initErr
}

// QueryClusterPerf fetches historical CPU and memory performance data for a cluster.
func (p *PerfClient) QueryClusterPerf(ctx context.Context, clusterMOR types.ManagedObjectReference, start, end time.Time, intervalID int32) (*ClusterPerfResult, error) {
	if err := p.resolveCounters(ctx); err != nil {
		return nil, err
	}

	perfMgr := p.client.ServiceContent.PerfManager

	cpuCounterID, ok := p.counters["cpu.usagemhz.average"]
	if !ok {
		return nil, fmt.Errorf("counter cpu.usagemhz.average not found")
	}
	memCounterID, ok := p.counters["mem.consumed.average"]
	if !ok {
		return nil, fmt.Errorf("counter mem.consumed.average not found")
	}

	spec := types.PerfQuerySpec{
		Entity:     clusterMOR,
		StartTime:  &start,
		EndTime:    &end,
		IntervalId: intervalID,
		MetricId: []types.PerfMetricId{
			{CounterId: cpuCounterID, Instance: ""},
			{CounterId: memCounterID, Instance: ""},
		},
	}

	resp, err := methods.QueryPerf(ctx, p.client, &types.QueryPerf{
		This:      *perfMgr,
		QuerySpec: []types.PerfQuerySpec{spec},
	})
	if err != nil {
		return nil, fmt.Errorf("querying perf data: %w", err)
	}

	if len(resp.Returnval) == 0 {
		return &ClusterPerfResult{}, nil
	}

	em, ok := resp.Returnval[0].(*types.PerfEntityMetric)
	if !ok {
		return nil, fmt.Errorf("unexpected perf response type %T", resp.Returnval[0])
	}

	result := &ClusterPerfResult{
		Timestamps: make([]int64, len(em.SampleInfo)),
	}
	for i, si := range em.SampleInfo {
		result.Timestamps[i] = si.Timestamp.Unix()
	}

	for _, v := range em.Value {
		series, ok := v.(*types.PerfMetricIntSeries)
		if !ok {
			continue
		}
		switch series.Id.CounterId {
		case cpuCounterID:
			result.CpuUsageMHz = series.Value
		case memCounterID:
			result.MemConsumedKB = series.Value
		}
	}

	return result, nil
}

// IntervalForRange returns the vSphere historical statistics interval
// appropriate for the given graph range.
func IntervalForRange(rangeStr string) int32 {
	switch rangeStr {
	case "7d":
		return 1800
	default:
		return 300
	}
}
