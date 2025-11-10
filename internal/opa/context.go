package opa

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// AuthorizationInput represents the complete input sent to OPA for authorization decisions
type AuthorizationInput struct {
	// User information
	User UserContext `json:"user"`

	// Resource being accessed
	Resource ResourceContext `json:"resource"`

	// Action being performed
	Action string `json:"action"`

	// Time-based attributes
	Time TimeContext `json:"time"`

	// Contextual attributes
	Context RequestContext `json:"context"`

	// Tenant information
	Tenant TenantContext `json:"tenant"`
}

// UserContext contains user-related attributes
type UserContext struct {
	ID          string   `json:"id"`
	Email       string   `json:"email"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions,omitempty"`
	TenantID    string   `json:"tenantId"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// ResourceContext contains resource-related attributes
type ResourceContext struct {
	Type     string                 `json:"type"`     // e.g., "user", "role", "policy"
	ID       string                 `json:"id,omitempty"`
	OwnerID  string                 `json:"ownerId,omitempty"`
	TenantID string                 `json:"tenantId,omitempty"`
	Attributes map[string]interface{} `json:"attributes,omitempty"`
}

// TimeContext contains time-based attributes for ABAC
type TimeContext struct {
	Timestamp   time.Time `json:"timestamp"`
	DayOfWeek   string    `json:"dayOfWeek"`   // "Monday", "Tuesday", etc.
	Hour        int       `json:"hour"`        // 0-23
	Minute      int       `json:"minute"`      // 0-59
	IsWeekend   bool      `json:"isWeekend"`
	IsBusinessHours bool  `json:"isBusinessHours"` // 9 AM - 5 PM weekdays
}

// RequestContext contains contextual request information
type RequestContext struct {
	IPAddress    string            `json:"ipAddress"`
	UserAgent    string            `json:"userAgent"`
	Method       string            `json:"method"` // HTTP method
	Path         string            `json:"path"`   // Request path
	MFAVerified  bool              `json:"mfaVerified"`
	SessionAge   int64             `json:"sessionAge"` // Age in seconds
	Headers      map[string]string `json:"headers,omitempty"`
}

// TenantContext contains tenant-related information
type TenantContext struct {
	ID       string                 `json:"id"`
	Slug     string                 `json:"slug,omitempty"`
	Settings map[string]interface{} `json:"settings,omitempty"`
}

// ContextBuilder helps build the authorization input from various sources
type ContextBuilder struct {
	input *AuthorizationInput
}

// NewContextBuilder creates a new context builder
func NewContextBuilder() *ContextBuilder {
	now := time.Now()
	return &ContextBuilder{
		input: &AuthorizationInput{
			Time: TimeContext{
				Timestamp:       now,
				DayOfWeek:       now.Weekday().String(),
				Hour:            now.Hour(),
				Minute:          now.Minute(),
				IsWeekend:       now.Weekday() == time.Saturday || now.Weekday() == time.Sunday,
				IsBusinessHours: isBusinessHours(now),
			},
		},
	}
}

// NewContextBuilderFromFiber creates a context builder from a Fiber context
func NewContextBuilderFromFiber(c *fiber.Ctx) *ContextBuilder {
	builder := NewContextBuilder()

	// Extract user information from Fiber locals
	if userID, ok := c.Locals("userID").(string); ok {
		builder.input.User.ID = userID
	}

	if email, ok := c.Locals("email").(string); ok {
		builder.input.User.Email = email
	}

	if roles, ok := c.Locals("roles").([]string); ok {
		builder.input.User.Roles = roles
	}

	if tenantID, ok := c.Locals("tenantID").(string); ok {
		builder.input.User.TenantID = tenantID
		builder.input.Tenant.ID = tenantID
	}

	// Extract request context
	builder.input.Context.IPAddress = c.IP()
	builder.input.Context.UserAgent = c.Get("User-Agent")
	builder.input.Context.Method = c.Method()
	builder.input.Context.Path = c.Path()

	// Extract MFA status if available
	if mfaVerified, ok := c.Locals("mfaVerified").(bool); ok {
		builder.input.Context.MFAVerified = mfaVerified
	}

	return builder
}

// WithUser sets user information
func (b *ContextBuilder) WithUser(userID, email string, roles []string) *ContextBuilder {
	b.input.User.ID = userID
	b.input.User.Email = email
	b.input.User.Roles = roles
	return b
}

// WithUserPermissions adds user permissions
func (b *ContextBuilder) WithUserPermissions(permissions []string) *ContextBuilder {
	b.input.User.Permissions = permissions
	return b
}

// WithUserMetadata adds user metadata
func (b *ContextBuilder) WithUserMetadata(metadata map[string]interface{}) *ContextBuilder {
	b.input.User.Metadata = metadata
	return b
}

// WithResource sets resource information
func (b *ContextBuilder) WithResource(resourceType, resourceID string) *ContextBuilder {
	b.input.Resource.Type = resourceType
	b.input.Resource.ID = resourceID
	return b
}

// WithResourceOwner sets the resource owner
func (b *ContextBuilder) WithResourceOwner(ownerID string) *ContextBuilder {
	b.input.Resource.OwnerID = ownerID
	return b
}

// WithResourceTenant sets the resource tenant
func (b *ContextBuilder) WithResourceTenant(tenantID string) *ContextBuilder {
	b.input.Resource.TenantID = tenantID
	return b
}

// WithResourceAttributes adds resource attributes
func (b *ContextBuilder) WithResourceAttributes(attributes map[string]interface{}) *ContextBuilder {
	b.input.Resource.Attributes = attributes
	return b
}

// WithAction sets the action being performed
func (b *ContextBuilder) WithAction(action string) *ContextBuilder {
	b.input.Action = action
	return b
}

// WithTenant sets tenant information
func (b *ContextBuilder) WithTenant(tenantID, slug string, settings map[string]interface{}) *ContextBuilder {
	b.input.Tenant.ID = tenantID
	b.input.Tenant.Slug = slug
	b.input.Tenant.Settings = settings
	return b
}

// WithIPAddress sets the IP address
func (b *ContextBuilder) WithIPAddress(ip string) *ContextBuilder {
	b.input.Context.IPAddress = ip
	return b
}

// WithMFAVerified sets MFA verification status
func (b *ContextBuilder) WithMFAVerified(verified bool) *ContextBuilder {
	b.input.Context.MFAVerified = verified
	return b
}

// WithSessionAge sets the session age
func (b *ContextBuilder) WithSessionAge(ageInSeconds int64) *ContextBuilder {
	b.input.Context.SessionAge = ageInSeconds
	return b
}

// WithRequestHeaders adds request headers
func (b *ContextBuilder) WithRequestHeaders(headers map[string]string) *ContextBuilder {
	b.input.Context.Headers = headers
	return b
}

// Build returns the authorization input
func (b *ContextBuilder) Build() map[string]interface{} {
	// Convert struct to map for OPA
	result := make(map[string]interface{})

	// Add all fields
	result["user"] = map[string]interface{}{
		"id":          b.input.User.ID,
		"email":       b.input.User.Email,
		"roles":       b.input.User.Roles,
		"permissions": b.input.User.Permissions,
		"tenantId":    b.input.User.TenantID,
		"metadata":    b.input.User.Metadata,
	}

	result["resource"] = map[string]interface{}{
		"type":       b.input.Resource.Type,
		"id":         b.input.Resource.ID,
		"ownerId":    b.input.Resource.OwnerID,
		"tenantId":   b.input.Resource.TenantID,
		"attributes": b.input.Resource.Attributes,
	}

	result["action"] = b.input.Action

	result["time"] = map[string]interface{}{
		"timestamp":       b.input.Time.Timestamp.Unix(),
		"dayOfWeek":       b.input.Time.DayOfWeek,
		"hour":            b.input.Time.Hour,
		"minute":          b.input.Time.Minute,
		"isWeekend":       b.input.Time.IsWeekend,
		"isBusinessHours": b.input.Time.IsBusinessHours,
	}

	result["context"] = map[string]interface{}{
		"ipAddress":   b.input.Context.IPAddress,
		"userAgent":   b.input.Context.UserAgent,
		"method":      b.input.Context.Method,
		"path":        b.input.Context.Path,
		"mfaVerified": b.input.Context.MFAVerified,
		"sessionAge":  b.input.Context.SessionAge,
		"headers":     b.input.Context.Headers,
	}

	result["tenant"] = map[string]interface{}{
		"id":       b.input.Tenant.ID,
		"slug":     b.input.Tenant.Slug,
		"settings": b.input.Tenant.Settings,
	}

	return result
}

// isBusinessHours checks if the given time is during business hours (9 AM - 5 PM on weekdays)
func isBusinessHours(t time.Time) bool {
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return false
	}
	hour := t.Hour()
	return hour >= 9 && hour < 17
}

// BuildSimpleInput creates a simple authorization input for basic permission checks
func BuildSimpleInput(userID, tenantID, action, resourceType, resourceID string) map[string]interface{} {
	builder := NewContextBuilder()
	builder.WithUser(userID, "", []string{})
	builder.WithAction(action)
	builder.WithResource(resourceType, resourceID)
	builder.WithTenant(tenantID, "", nil)

	return builder.Build()
}

// BuildPermissionCheckInput creates input for checking if a user has a specific permission
func BuildPermissionCheckInput(userID, tenantID string, roles []string, resource, action string) map[string]interface{} {
	builder := NewContextBuilder()
	builder.WithUser(userID, "", roles)
	builder.WithAction(action)
	builder.WithResource(resource, "")
	builder.WithTenant(tenantID, "", nil)

	return builder.Build()
}

// BuildOwnershipCheckInput creates input for checking resource ownership
func BuildOwnershipCheckInput(userID, tenantID, resourceType, resourceID, ownerID, action string) map[string]interface{} {
	builder := NewContextBuilder()
	builder.WithUser(userID, "", []string{})
	builder.WithAction(action)
	builder.WithResource(resourceType, resourceID)
	builder.WithResourceOwner(ownerID)
	builder.WithTenant(tenantID, "", nil)

	return builder.Build()
}

// ParseUUID safely parses a UUID string
func ParseUUID(s string) string {
	if _, err := uuid.Parse(s); err != nil {
		return ""
	}
	return s
}

// GetUserIDFromContext extracts user ID from context
func GetUserIDFromContext(ctx context.Context) string {
	if userID := ctx.Value("userID"); userID != nil {
		if id, ok := userID.(string); ok {
			return id
		}
	}
	return ""
}

// GetTenantIDFromContext extracts tenant ID from context
func GetTenantIDFromContext(ctx context.Context) string {
	if tenantID := ctx.Value("tenantID"); tenantID != nil {
		if id, ok := tenantID.(string); ok {
			return id
		}
	}
	return ""
}
