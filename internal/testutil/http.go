package testutil

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// HTTPTestCase represents a test case for HTTP endpoints
type HTTPTestCase struct {
	Name           string
	Method         string
	Path           string
	Body           interface{}
	Headers        map[string]string
	ExpectedStatus int
	ExpectedBody   map[string]interface{}
	CheckResponse  func(t *testing.T, resp *fiber.Response)
}

// CreateTestApp creates a Fiber app for testing
func CreateTestApp() *fiber.App {
	return fiber.New(fiber.Config{
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			if e, ok := err.(*fiber.Error); ok {
				code = e.Code
			}
			return c.Status(code).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": err.Error(),
				},
			})
		},
	})
}

// MakeRequest makes a test HTTP request
func MakeRequest(t *testing.T, app *fiber.App, method, path string, body interface{}, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("Failed to marshal request body: %v", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")

	// Add custom headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}

	recorder := httptest.NewRecorder()
	recorder.Code = resp.StatusCode

	// Copy response body
	bodyBytes, _ := io.ReadAll(resp.Body)
	recorder.Body = bytes.NewBuffer(bodyBytes)

	return recorder
}

// ParseJSONResponse parses a JSON response into a map
func ParseJSONResponse(t *testing.T, resp *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	return result
}

// AssertStatusCode asserts the HTTP status code
func AssertStatusCode(t *testing.T, expected, actual int) {
	t.Helper()

	if expected != actual {
		t.Errorf("Expected status code %d, got %d", expected, actual)
	}
}

// AssertJSONField asserts a field in a JSON response
func AssertJSONField(t *testing.T, resp map[string]interface{}, field string, expected interface{}) {
	t.Helper()

	actual, ok := resp[field]
	if !ok {
		t.Errorf("Field '%s' not found in response", field)
		return
	}

	if actual != expected {
		t.Errorf("Field '%s': expected %v, got %v", field, expected, actual)
	}
}

// AssertJSONSuccess asserts the response has success: true
func AssertJSONSuccess(t *testing.T, resp map[string]interface{}) {
	t.Helper()

	success, ok := resp["success"].(bool)
	if !ok {
		t.Error("'success' field not found or not a boolean")
		return
	}

	if !success {
		t.Error("Expected success: true, got false")
		if errMap, ok := resp["error"].(map[string]interface{}); ok {
			if msg, ok := errMap["message"].(string); ok {
				t.Logf("Error message: %s", msg)
			}
		}
	}
}

// AssertJSONError asserts the response has success: false
func AssertJSONError(t *testing.T, resp map[string]interface{}, expectedCode string) {
	t.Helper()

	success, ok := resp["success"].(bool)
	if !ok {
		t.Error("'success' field not found or not a boolean")
		return
	}

	if success {
		t.Error("Expected success: false, got true")
		return
	}

	if expectedCode != "" {
		errMap, ok := resp["error"].(map[string]interface{})
		if !ok {
			t.Error("'error' field not found or not a map")
			return
		}

		code, ok := errMap["code"].(string)
		if !ok {
			t.Error("'error.code' field not found or not a string")
			return
		}

		if code != expectedCode {
			t.Errorf("Expected error code '%s', got '%s'", expectedCode, code)
		}
	}
}

// GetDataField extracts the 'data' field from a response
func GetDataField(t *testing.T, resp map[string]interface{}) map[string]interface{} {
	t.Helper()

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("'data' field not found or not a map")
	}

	return data
}

// WithAuthHeader returns headers with Authorization
func WithAuthHeader(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
	}
}

// WithTenantHeader returns headers with X-Tenant-ID
func WithTenantHeader(tenantID string) map[string]string {
	return map[string]string{
		"X-Tenant-ID": tenantID,
	}
}

// WithHeaders combines multiple header maps
func WithHeaders(headerMaps ...map[string]string) map[string]string {
	result := make(map[string]string)
	for _, headers := range headerMaps {
		for key, value := range headers {
			result[key] = value
		}
	}
	return result
}
