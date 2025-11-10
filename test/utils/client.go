package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// TestClient is a helper HTTP client for integration tests
type TestClient struct {
	BaseURL    string
	HTTPClient *http.Client
	AuthToken  string
}

// NewTestClient creates a new test client
func NewTestClient(baseURL string) *TestClient {
	return &TestClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// SetAuthToken sets the authorization token
func (c *TestClient) SetAuthToken(token string) {
	c.AuthToken = token
}

// Request makes an HTTP request
func (c *TestClient) Request(method, path string, body interface{}, headers map[string]string) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewBuffer(jsonData)
	}

	req, err := http.NewRequest(method, c.BaseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set default headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Set auth token if available
	if c.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}

	// Set custom headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	return c.HTTPClient.Do(req)
}

// DecodeResponse decodes JSON response
func (c *TestClient) DecodeResponse(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// AssertStatusCode asserts HTTP status code
func AssertStatusCode(t *testing.T, expected, actual int, msgAndArgs ...interface{}) {
	t.Helper()
	if expected != actual {
		msg := fmt.Sprintf("Expected status code %d but got %d", expected, actual)
		if len(msgAndArgs) > 0 {
			msg = fmt.Sprintf("%s: %v", msg, msgAndArgs)
		}
		t.Fatalf(msg)
	}
}

// AssertNoError asserts no error occurred
func AssertNoError(t *testing.T, err error, msgAndArgs ...interface{}) {
	t.Helper()
	if err != nil {
		msg := fmt.Sprintf("Expected no error but got: %v", err)
		if len(msgAndArgs) > 0 {
			msg = fmt.Sprintf("%s: %v", msg, msgAndArgs)
		}
		t.Fatalf(msg)
	}
}

// AssertEqual asserts two values are equal
func AssertEqual(t *testing.T, expected, actual interface{}, msgAndArgs ...interface{}) {
	t.Helper()
	if expected != actual {
		msg := fmt.Sprintf("Expected %v but got %v", expected, actual)
		if len(msgAndArgs) > 0 {
			msg = fmt.Sprintf("%s: %v", msg, msgAndArgs)
		}
		t.Fatalf(msg)
	}
}

// AssertNotEmpty asserts string is not empty
func AssertNotEmpty(t *testing.T, value string, msgAndArgs ...interface{}) {
	t.Helper()
	if value == "" {
		msg := "Expected non-empty string"
		if len(msgAndArgs) > 0 {
			msg = fmt.Sprintf("%s: %v", msg, msgAndArgs)
		}
		t.Fatalf(msg)
	}
}

// AssertTrue asserts condition is true
func AssertTrue(t *testing.T, condition bool, msgAndArgs ...interface{}) {
	t.Helper()
	if !condition {
		msg := "Expected condition to be true"
		if len(msgAndArgs) > 0 {
			msg = fmt.Sprintf("%s: %v", msg, msgAndArgs)
		}
		t.Fatalf(msg)
	}
}

// AssertFalse asserts condition is false
func AssertFalse(t *testing.T, condition bool, msgAndArgs ...interface{}) {
	t.Helper()
	if condition {
		msg := "Expected condition to be false"
		if len(msgAndArgs) > 0 {
			msg = fmt.Sprintf("%s: %v", msg, msgAndArgs)
		}
		t.Fatalf(msg)
	}
}
