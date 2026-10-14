package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/prometheus"
	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/store"
	vcmv1 "github.com/openshift-splat-team/vsphere-capacity-manager/pkg/apis/vspherecapacitymanager.splat.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeNetworkUsageProm returns constant-valued series for several pools at
// whatever evaluation grid the dashboard requested, so snapped grid values are
// deterministic regardless of grid alignment.
func fakeNetworkUsageProm(t *testing.T, gotQuery *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		*gotQuery = q.Get("query")

		startUnix, err := strconv.ParseInt(q.Get("start"), 10, 64)
		if err != nil {
			t.Errorf("invalid start param: %v", err)
			http.Error(w, "bad start", http.StatusBadRequest)
			return
		}
		stepSec, err := strconv.ParseInt(q.Get("step"), 10, 64)
		if err != nil {
			t.Errorf("invalid step param: %v", err)
			http.Error(w, "bad step", http.StatusBadRequest)
			return
		}
		if startUnix%stepSec != 0 {
			t.Errorf("expected step-aligned start, got start=%d step=%d", startUnix, stepSec)
		}

		var values strings.Builder
		values.WriteString("[")
		for i := 0; i < 5; i++ {
			if i > 0 {
				values.WriteString(",")
			}
			fmt.Fprintf(&values, "[%d,\"42\"]", startUnix+int64(i)*stepSec)
		}
		values.WriteString("]")

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"status": "success",
			"data": {"resultType": "matrix", "result": [
				{"metric": {"namespace": "vsphere-infra-helpers", "pool": "pool-a", "networkType": "single-tenant"}, "values": %s},
				{"metric": {"namespace": "vsphere-infra-helpers", "pool": "pool-a", "networkType": "multi-tenant"}, "values": %s},
				{"metric": {"namespace": "vsphere-infra-helpers", "pool": "excluded-pool", "networkType": "single-tenant"}, "values": %s},
				{"metric": {"namespace": "vsphere-infra-helpers", "pool": "pool-b", "networkType": "disconnected"}, "values": %s}
			]}
		}`, values.String(), values.String(), values.String(), values.String())
	}))
}

func TestHandleNetworkGraph(t *testing.T) {
	var gotQuery string
	promSrv := fakeNetworkUsageProm(t, &gotQuery)
	defer promSrv.Close()

	st := store.New()
	st.SetPool(&vcmv1.Pool{
		ObjectMeta: metav1.ObjectMeta{Name: "excluded-pool"},
		Spec:       vcmv1.PoolSpec{Exclude: true},
	})

	srv, err := New(st, "", prometheus.New(promSrv.URL, "", true), nil)
	if err != nil {
		t.Fatalf("creating server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/graph/networks?range=24h", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp NetworkGraphResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parsing response: %v", err)
	}

	if !strings.Contains(gotQuery, "max by (namespace, pool, networkType)") {
		t.Errorf("expected instance-collapsing aggregation in query, got: %s", gotQuery)
	}
	if !strings.Contains(gotQuery, "pool_networks_total_by_type") || !strings.Contains(gotQuery, "pool_networks_available_by_type") {
		t.Errorf("expected by-type network metrics in query, got: %s", gotQuery)
	}

	// pool-a survives; excluded-pool is filtered via spec.exclude; the
	// disconnected networkType series is dropped.
	if len(resp.Pools) != 1 {
		t.Fatalf("expected 1 pool, got %d: %+v", len(resp.Pools), resp.Pools)
	}
	if resp.Pools[0].PoolName != "pool-a" {
		t.Errorf("expected pool-a, got %s", resp.Pools[0].PoolName)
	}

	if len(resp.Timestamps) == 0 {
		t.Fatal("expected timestamps in response")
	}
	if len(resp.Pools[0].SingleTenantPct) != len(resp.Timestamps) {
		t.Errorf("single-tenant series length %d != timestamps length %d", len(resp.Pools[0].SingleTenantPct), len(resp.Timestamps))
	}
	if len(resp.Pools[0].MultiTenantPct) != len(resp.Timestamps) {
		t.Errorf("multi-tenant series length %d != timestamps length %d", len(resp.Pools[0].MultiTenantPct), len(resp.Timestamps))
	}

	// The fake emits 5 evaluation points at the window start; every later
	// grid point must be a null gap, not a zero or a carried-forward value.
	for field, series := range map[string][]*float64{
		"single_tenant_pct": resp.Pools[0].SingleTenantPct,
		"multi_tenant_pct":  resp.Pools[0].MultiTenantPct,
	} {
		nonNull := 0
		for i, v := range series {
			if v == nil {
				continue
			}
			nonNull++
			if *v != 42 {
				t.Errorf("%s[%d] = %f, want 42", field, i, *v)
			}
		}
		if nonNull != 5 {
			t.Errorf("%s: expected 5 snapped points, got %d", field, nonNull)
		}
	}
}

func TestHandleNetworkGraphNoClient(t *testing.T) {
	srv, err := New(store.New(), "", nil, nil)
	if err != nil {
		t.Fatalf("creating server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/graph/networks", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp NetworkGraphResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parsing response: %v", err)
	}
	if resp.Range != "24h" {
		t.Errorf("expected default range 24h, got %s", resp.Range)
	}
	// Without a Prometheus client there is no pool data; only the (empty)
	// grid is emitted.
	if len(resp.Pools) != 0 {
		t.Errorf("expected no pools without Prometheus client, got %+v", resp.Pools)
	}
}

func TestHandleNetworkGraphInvalidRange(t *testing.T) {
	srv, err := New(store.New(), "", nil, nil)
	if err != nil {
		t.Fatalf("creating server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/graph/networks?range=1y", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid range, got %d", w.Code)
	}
}

func TestAlignWindow(t *testing.T) {
	end := time.Unix(1791990123, 0)
	windowStart := end.Add(-7 * 24 * time.Hour)

	alignedStart, alignedEnd := alignWindow(windowStart, end, 30*time.Minute)

	if alignedStart.Unix() > windowStart.Unix() || alignedEnd.Unix() > end.Unix() {
		t.Errorf("aligned window (%d, %d) is not floored within the original window (%d, %d)",
			alignedStart.Unix(), alignedEnd.Unix(), windowStart.Unix(), end.Unix())
	}

	// Every chart step (60s, 5m, 10m) divides the coarsest one (30m), so an
	// instant aligned for the 7d chart is also an evaluation instant of every
	// finer range: 09:00:00 reads the same value on all charts.
	for _, step := range []int64{1800, 600, 300, 60} {
		if alignedStart.Unix()%step != 0 || alignedEnd.Unix()%step != 0 {
			t.Errorf("7d-aligned window (%d, %d) not aligned to %ds step",
				alignedStart.Unix(), alignedEnd.Unix(), step)
		}
	}
}

func TestOverviewRendersNetworkSection(t *testing.T) {
	srv, err := New(store.New(), "", nil, nil)
	if err != nil {
		t.Fatalf("creating server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	for _, id := range []string{
		"network-usage-section",
		"network-st-chart", "network-mt-chart",
	} {
		if !strings.Contains(body, id) {
			t.Errorf("overview page missing element %q", id)
		}
	}
}
