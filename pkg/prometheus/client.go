package prometheus

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

const defaultTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// TimeSeriesPoint represents a single data point in a time series.
type TimeSeriesPoint struct {
	Timestamp int64   `json:"ts"`
	Value     float64 `json:"v"`
}

// Series is a single labeled result series from a range query.
type Series struct {
	Labels map[string]string
	Points []TimeSeriesPoint
}

// Client queries Prometheus/Thanos range API with ServiceAccount bearer token auth.
type Client struct {
	baseURL    string
	httpClient *http.Client
	tokenPath  string
}

// New creates a Prometheus query client.
func New(baseURL, tokenPath string, skipTLSVerify bool) *Client {
	if tokenPath == "" {
		tokenPath = defaultTokenPath
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: skipTLSVerify},
	}
	return &Client{
		baseURL:   baseURL,
		tokenPath: tokenPath,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}
}

// QueryRange executes a Prometheus range query and returns the first result series.
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]TimeSeriesPoint, error) {
	series, err := c.QueryRangeSeries(ctx, query, start, end, step)
	if err != nil {
		return nil, err
	}
	if len(series) == 0 {
		return nil, nil
	}
	return series[0].Points, nil
}

// QueryRangeSeries executes a Prometheus range query and returns every result
// series with its labels.
func (c *Client) QueryRangeSeries(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]Series, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", strconv.FormatInt(start.Unix(), 10))
	params.Set("end", strconv.FormatInt(end.Unix(), 10))
	params.Set("step", strconv.FormatInt(int64(step.Seconds()), 10))
	// Force raw samples. A Thanos querier with --query.auto-downsampling
	// derives its source resolution from the step (step/5), which silently
	// switches coarser queries (e.g. a 30m step) onto downsampled data and
	// yields values inconsistent with finer ranges. Plain Prometheus ignores
	// this parameter. Dashboard query windows are at most 7d, within typical
	// raw retention.
	params.Set("max_source_resolution", "0s")

	reqURL := fmt.Sprintf("%s/api/v1/query_range?%s", c.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	token, err := c.readToken()
	if err == nil && token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	return parseMatrixSeries(body)
}

func (c *Client) readToken() (string, error) {
	data, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type promResponse struct {
	Status string   `json:"status"`
	Data   promData `json:"data"`
}

type promData struct {
	ResultType string             `json:"resultType"`
	Result     []promMatrixResult `json:"result"`
}

type promMatrixResult struct {
	Metric map[string]string `json:"metric"`
	Values [][]interface{}   `json:"values"`
}

func parseMatrixSeries(body []byte) ([]Series, error) {
	var resp promResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("query returned status: %s", resp.Status)
	}
	if resp.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("unexpected result type: %s", resp.Data.ResultType)
	}

	series := make([]Series, 0, len(resp.Data.Result))
	for _, result := range resp.Data.Result {
		series = append(series, Series{
			Labels: result.Metric,
			Points: parseValues(result.Values),
		})
	}
	return series, nil
}

func parseValues(values [][]interface{}) []TimeSeriesPoint {
	points := make([]TimeSeriesPoint, 0, len(values))
	for _, pair := range values {
		if len(pair) != 2 {
			continue
		}
		ts, ok := pair[0].(float64)
		if !ok {
			continue
		}
		valStr, ok := pair[1].(string)
		if !ok {
			continue
		}
		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			continue
		}
		points = append(points, TimeSeriesPoint{
			Timestamp: int64(ts),
			Value:     val,
		})
	}
	return points
}
