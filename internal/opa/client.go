package opa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/techsavvyash/heimdall/internal/config"
)

// Client represents an OPA HTTP client
type Client struct {
	baseURL    string
	policyPath string
	httpClient *http.Client
	timeout    time.Duration
}

// NewClient creates a new OPA client
func NewClient(cfg *config.OPAConfig) *Client {
	return &Client{
		baseURL:    cfg.URL,
		policyPath: cfg.PolicyPath,
		timeout:    cfg.Timeout,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// DecisionRequest represents a request to OPA for an authorization decision
type DecisionRequest struct {
	Input map[string]interface{} `json:"input"`
}

// DecisionResponse represents OPA's response to an authorization query
type DecisionResponse struct {
	Result         interface{}   `json:"result"`
	DecisionID     string        `json:"decision_id,omitempty"`
	Provenance     *Provenance   `json:"provenance,omitempty"`
	Metrics        *Metrics      `json:"metrics,omitempty"`
}

// Provenance contains information about the decision
type Provenance struct {
	Version  string   `json:"version,omitempty"`
	Build    string   `json:"build,omitempty"`
	Revision string   `json:"revision,omitempty"`
}

// Metrics contains performance metrics for the decision
type Metrics struct {
	TimerRego   float64 `json:"timer_rego_input_parse_ns,omitempty"`
	TimerCompile float64 `json:"timer_compile_module_ns,omitempty"`
	TimerEval   float64 `json:"timer_rego_query_eval_ns,omitempty"`
}

// EvaluatePolicy evaluates a policy at a specific path
func (c *Client) EvaluatePolicy(ctx context.Context, policyPath string, input map[string]interface{}) (*DecisionResponse, error) {
	url := fmt.Sprintf("%s/v1/data/%s", c.baseURL, policyPath)

	req := DecisionRequest{Input: input}
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OPA returned status %d: %s", resp.StatusCode, string(body))
	}

	var decision DecisionResponse
	if err := json.Unmarshal(body, &decision); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &decision, nil
}

// Evaluate evaluates the default policy path
func (c *Client) Evaluate(ctx context.Context, input map[string]interface{}) (*DecisionResponse, error) {
	return c.EvaluatePolicy(ctx, c.policyPath, input)
}

// CheckPermission is a convenience method to check if an action is allowed
func (c *Client) CheckPermission(ctx context.Context, input map[string]interface{}) (bool, error) {
	decision, err := c.Evaluate(ctx, input)
	if err != nil {
		return false, err
	}

	// Try to extract boolean result
	if result, ok := decision.Result.(bool); ok {
		return result, nil
	}

	// Try to extract from allow field
	if resultMap, ok := decision.Result.(map[string]interface{}); ok {
		if allow, ok := resultMap["allow"].(bool); ok {
			return allow, nil
		}
	}

	return false, fmt.Errorf("unexpected result format: %v", decision.Result)
}

// HealthCheck checks if OPA is healthy
func (c *Client) HealthCheck(ctx context.Context) error {
	url := fmt.Sprintf("%s/health", c.baseURL)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to execute health check: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OPA health check failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ListPolicies lists all loaded policies in OPA
func (c *Client) ListPolicies(ctx context.Context) (map[string]interface{}, error) {
	url := fmt.Sprintf("%s/v1/policies", c.baseURL)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OPA returned status %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return result, nil
}

// GetData retrieves data at a specific path from OPA
func (c *Client) GetData(ctx context.Context, path string) (interface{}, error) {
	url := fmt.Sprintf("%s/v1/data/%s", c.baseURL, path)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OPA returned status %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return result["result"], nil
}

// PutData pushes data to OPA at a specific path
func (c *Client) PutData(ctx context.Context, path string, data interface{}) error {
	url := fmt.Sprintf("%s/v1/data/%s", c.baseURL, path)

	reqBody, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewBuffer(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OPA returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// DeleteData deletes data at a specific path
func (c *Client) DeleteData(ctx context.Context, path string) error {
	url := fmt.Sprintf("%s/v1/data/%s", c.baseURL, path)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OPA returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
