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
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", strconv.FormatInt(start.Unix(), 10))
	params.Set("end", strconv.FormatInt(end.Unix(), 10))
	params.Set("step", strconv.FormatInt(int64(step.Seconds()), 10))

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

	return parseMatrixResponse(body)
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

func parseMatrixResponse(body []byte) ([]TimeSeriesPoint, error) {
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
	if len(resp.Data.Result) == 0 {
		return nil, nil
	}

	values := resp.Data.Result[0].Values
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
	return points, nil
}
