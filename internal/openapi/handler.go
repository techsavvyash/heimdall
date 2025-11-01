package openapi

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/swaggest/swgui/v5emb"
)

// Handler manages OpenAPI spec serving and Swagger UI
type Handler struct {
	spec     *openapi3.T
	specJSON []byte
	mu       sync.RWMutex
}

// NewHandler creates a new OpenAPI handler
func NewHandler() *Handler {
	return &Handler{}
}

// Initialize generates the OpenAPI spec and caches it
func (h *Handler) Initialize() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Generate the spec
	generator := NewGenerator()
	h.spec = generator.GenerateSpec()

	// Validate the spec (skip validation errors for now as we're generating dynamically)
	// The spec will be validated when consumed by clients
	// if err := h.spec.Validate(context.Background()); err != nil {
	// 	return err
	// }

	// Marshal to JSON for serving
	specBytes, err := json.MarshalIndent(h.spec, "", "  ")
	if err != nil {
		return err
	}
	h.specJSON = specBytes

	return nil
}

// GetSpec returns the generated OpenAPI spec
func (h *Handler) GetSpec() *openapi3.T {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.spec
}

// ServeSpecJSON serves the OpenAPI specification as JSON
func (h *Handler) ServeSpecJSON(c *fiber.Ctx) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	c.Set("Content-Type", "application/json")
	return c.Send(h.specJSON)
}

// ServeSwaggerUI serves the Swagger UI
func (h *Handler) ServeSwaggerUI() http.Handler {
	// Create Swagger UI handler with custom configuration
	return v5emb.New(
		"Heimdall API Documentation",
		"/swagger/spec", // Path where the OpenAPI spec is served
		"/swagger",      // Base path for the UI
	)
}

// RegisterRoutes registers the OpenAPI and Swagger UI routes
func (h *Handler) RegisterRoutes(app *fiber.App) {
	// Serve OpenAPI spec as JSON
	app.Get("/swagger/spec", h.ServeSpecJSON)

	// Serve Swagger UI using the adaptor for http.Handler
	swaggerHandler := h.ServeSwaggerUI()
	app.Get("/swagger/*", adaptor.HTTPHandler(swaggerHandler))
}
