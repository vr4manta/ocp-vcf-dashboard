package prometheus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueryRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("query") == "" {
			t.Error("missing query parameter")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"status": "success",
			"data": {
				"resultType": "matrix",
				"result": [{
					"metric": {"pool": "test-pool"},
					"values": [
						[1700000000, "42.5"],
						[1700000300, "43.1"],
						[1700000600, "41.8"]
					]
				}]
			}
		}`))
	}))
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tokenFile, []byte("test-token"), 0600)

	client := New(srv.URL, tokenFile, true)

	start := time.Unix(1700000000, 0)
	end := time.Unix(1700000600, 0)
	points, err := client.QueryRange(context.Background(), `pool_cpus_total{pool="test-pool"}`, start, end, 5*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("expected 3 points, got %d", len(points))
	}
	if points[0].Timestamp != 1700000000 {
		t.Errorf("expected timestamp 1700000000, got %d", points[0].Timestamp)
	}
	if points[0].Value != 42.5 {
		t.Errorf("expected value 42.5, got %f", points[0].Value)
	}
}

func TestQueryRangeEmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"status": "success",
			"data": {
				"resultType": "matrix",
				"result": []
			}
		}`))
	}))
	defer srv.Close()

	client := New(srv.URL, "", true)
	points, err := client.QueryRange(context.Background(), "nonexistent_metric", time.Now().Add(-time.Hour), time.Now(), time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if points != nil {
		t.Errorf("expected nil points for empty result, got %d", len(points))
	}
}

func TestQueryRangeErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"invalid query"}`))
	}))
	defer srv.Close()

	client := New(srv.URL, "", true)
	_, err := client.QueryRange(context.Background(), "bad{", time.Now().Add(-time.Hour), time.Now(), time.Minute)
	if err == nil {
		t.Fatal("expected error for bad status code")
	}
}

func TestQueryRangeBearerToken(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tokenFile, []byte("my-secret-token"), 0600)

	client := New(srv.URL, tokenFile, true)
	client.QueryRange(context.Background(), "up", time.Now().Add(-time.Hour), time.Now(), time.Minute)

	if receivedAuth != "Bearer my-secret-token" {
		t.Errorf("expected 'Bearer my-secret-token', got %q", receivedAuth)
	}
}
