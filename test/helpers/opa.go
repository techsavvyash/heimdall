package helpers

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/test/utils"
)

// OPAAuthInput represents OPA authorization input
type OPAAuthInput struct {
	User     OPAUser     `json:"user"`
	Resource OPAResource `json:"resource"`
	Action   string      `json:"action"`
	Time     OPATime     `json:"time,omitempty"`
	Context  OPAContext  `json:"context,omitempty"`
	Tenant   OPATenant   `json:"tenant,omitempty"`
}

// OPAUser represents user in OPA input
type OPAUser struct {
	ID          string                 `json:"id"`
	Email       string                 `json:"email"`
	Roles       []string               `json:"roles"`
	Permissions []string               `json:"permissions,omitempty"`
	TenantID    string                 `json:"tenantId"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// OPAResource represents resource in OPA input
type OPAResource struct {
	Type       string                 `json:"type"`
	ID         string                 `json:"id,omitempty"`
	OwnerID    string                 `json:"ownerId,omitempty"`
	TenantID   string                 `json:"tenantId,omitempty"`
	Attributes map[string]interface{} `json:"attributes,omitempty"`
}

// OPATime represents time context in OPA input
type OPATime struct {
	Timestamp       int64  `json:"timestamp"`
	DayOfWeek       string `json:"dayOfWeek"`
	Hour            int    `json:"hour"`
	IsBusinessHours bool   `json:"isBusinessHours"`
	IsWeekend       bool   `json:"isWeekend"`
}

// OPAContext represents request context in OPA input
type OPAContext struct {
	IPAddress   string            `json:"ipAddress,omitempty"`
	UserAgent   string            `json:"userAgent,omitempty"`
	Method      string            `json:"method,omitempty"`
	Path        string            `json:"path,omitempty"`
	MFAVerified bool              `json:"mfaVerified,omitempty"`
	SessionAge  int               `json:"sessionAge,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

// OPATenant represents tenant in OPA input
type OPATenant struct {
	ID       string                 `json:"id"`
	Slug     string                 `json:"slug,omitempty"`
	Status   string                 `json:"status,omitempty"`
	Settings map[string]interface{} `json:"settings,omitempty"`
}

// OPAResponse represents OPA decision response
type OPAResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Decision bool                   `json:"decision"`
		Allow    bool                   `json:"allow"`
		Reason   string                 `json:"reason,omitempty"`
		Metadata map[string]interface{} `json:"metadata,omitempty"`
	} `json:"data,omitempty"`
	Error *ErrorResponse `json:"error,omitempty"`
}

// RoleRequest represents role creation/update request
type RoleRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions"`
	IsSystem    bool     `json:"isSystem,omitempty"`
}

// RoleResponse represents role API response
type RoleResponse struct {
	Success bool `json:"success"`
	Data    struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
		TenantID    string   `json:"tenantId"`
		IsSystem    bool     `json:"isSystem"`
	} `json:"data,omitempty"`
	Error *ErrorResponse `json:"error,omitempty"`
}

// PermissionRequest represents permission creation request
type PermissionRequest struct {
	Resource    string `json:"resource"`
	Action      string `json:"action"`
	Scope       string `json:"scope,omitempty"`
	Description string `json:"description,omitempty"`
}

// PermissionResponse represents permission API response
type PermissionResponse struct {
	Success bool `json:"success"`
	Data    struct {
		ID          string `json:"id"`
		Resource    string `json:"resource"`
		Action      string `json:"action"`
		Scope       string `json:"scope"`
		Description string `json:"description"`
		TenantID    string `json:"tenantId"`
	} `json:"data,omitempty"`
	Error *ErrorResponse `json:"error,omitempty"`
}

// PolicyRequest represents policy creation request
type PolicyRequest struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Path        string                 `json:"path"`
	Type        string                 `json:"type"`
	Content     string                 `json:"content"`
	Tags        []string               `json:"tags,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// PolicyResponse represents policy API response
type PolicyResponse struct {
	Success bool `json:"success"`
	Data    struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Version     int    `json:"version"`
		Path        string `json:"path"`
		Type        string `json:"type"`
		Status      string `json:"status"`
		TenantID    string `json:"tenantId"`
	} `json:"data,omitempty"`
	Error *ErrorResponse `json:"error,omitempty"`
}

// CreateRole creates a role with permissions
func CreateRole(t *testing.T, client *utils.TestClient, req RoleRequest) *RoleResponse {
	t.Helper()

	resp, err := client.Request(http.MethodPost, "/v1/roles", req, nil)
	utils.AssertNoError(t, err, "Failed to make create role request")

	var roleResp RoleResponse
	err = client.DecodeResponse(resp, &roleResp)
	utils.AssertNoError(t, err, "Failed to decode role response")

	return &roleResp
}

// CreatePermission creates a permission
func CreatePermission(t *testing.T, client *utils.TestClient, req PermissionRequest) *PermissionResponse {
	t.Helper()

	resp, err := client.Request(http.MethodPost, "/v1/permissions", req, nil)
	utils.AssertNoError(t, err, "Failed to make create permission request")

	var permResp PermissionResponse
	err = client.DecodeResponse(resp, &permResp)
	utils.AssertNoError(t, err, "Failed to decode permission response")

	return &permResp
}

// CreatePolicy creates a Rego policy
func CreatePolicy(t *testing.T, client *utils.TestClient, req PolicyRequest) *PolicyResponse {
	t.Helper()

	resp, err := client.Request(http.MethodPost, "/v1/policies", req, nil)
	utils.AssertNoError(t, err, "Failed to make create policy request")

	var policyResp PolicyResponse
	err = client.DecodeResponse(resp, &policyResp)
	utils.AssertNoError(t, err, "Failed to decode policy response")

	return &policyResp
}

// CheckAuthorization makes an authorization check request
func CheckAuthorization(t *testing.T, client *utils.TestClient, input OPAAuthInput) *OPAResponse {
	t.Helper()

	resp, err := client.Request(http.MethodPost, "/v1/authz/check", input, nil)
	utils.AssertNoError(t, err, "Failed to make authorization check request")

	var opaResp OPAResponse
	err = client.DecodeResponse(resp, &opaResp)
	utils.AssertNoError(t, err, "Failed to decode OPA response")

	return &opaResp
}

// AssertAuthorized asserts that authorization is granted
func AssertAuthorized(t *testing.T, resp *OPAResponse, context string) {
	t.Helper()

	utils.AssertTrue(t, resp.Success, fmt.Sprintf("%s: Expected success to be true", context))
	utils.AssertTrue(t, resp.Data.Decision || resp.Data.Allow, fmt.Sprintf("%s: Expected authorization to be granted", context))
}

// AssertDenied asserts that authorization is denied
func AssertDenied(t *testing.T, resp *OPAResponse, context string) {
	t.Helper()

	// Either success=false with error, or success=true with decision=false
	if resp.Success {
		utils.AssertFalse(t, resp.Data.Decision && resp.Data.Allow, fmt.Sprintf("%s: Expected authorization to be denied", context))
	}
	// If not successful, that's also a denial (could be 403)
}

// NewOPAInput creates a new OPA authorization input
func NewOPAInput(userID, email, tenantID, resourceType, resourceID, action string, roles []string) OPAAuthInput {
	now := time.Now()
	hour := now.Hour()
	dayOfWeek := now.Weekday().String()
	isBusinessHours := hour >= 9 && hour < 17 && dayOfWeek != "Saturday" && dayOfWeek != "Sunday"
	isWeekend := dayOfWeek == "Saturday" || dayOfWeek == "Sunday"

	return OPAAuthInput{
		User: OPAUser{
			ID:       userID,
			Email:    email,
			Roles:    roles,
			TenantID: tenantID,
		},
		Resource: OPAResource{
			Type:     resourceType,
			ID:       resourceID,
			TenantID: tenantID,
		},
		Action: action,
		Time: OPATime{
			Timestamp:       now.Unix(),
			DayOfWeek:       dayOfWeek,
			Hour:            hour,
			IsBusinessHours: isBusinessHours,
			IsWeekend:       isWeekend,
		},
		Context: OPAContext{
			IPAddress:   "127.0.0.1",
			UserAgent:   "test-client",
			Method:      "GET",
			MFAVerified: false,
			SessionAge:  60, // 1 minute old session
		},
		Tenant: OPATenant{
			ID:     tenantID,
			Status: "active",
		},
	}
}

// NewOPAInputWithOwner creates OPA input with ownership
func NewOPAInputWithOwner(userID, email, tenantID, resourceType, resourceID, ownerID, action string, roles []string) OPAAuthInput {
	input := NewOPAInput(userID, email, tenantID, resourceType, resourceID, action, roles)
	input.Resource.OwnerID = ownerID
	return input
}

// NewOPAInputWithContext creates OPA input with custom context
func NewOPAInputWithContext(userID, email, tenantID, resourceType, resourceID, action string, roles []string, context OPAContext) OPAAuthInput {
	input := NewOPAInput(userID, email, tenantID, resourceType, resourceID, action, roles)
	input.Context = context
	return input
}

// NewOPAInputWithMetadata creates OPA input with user metadata
func NewOPAInputWithMetadata(userID, email, tenantID, resourceType, resourceID, action string, roles []string, metadata map[string]interface{}) OPAAuthInput {
	input := NewOPAInput(userID, email, tenantID, resourceType, resourceID, action, roles)
	input.User.Metadata = metadata
	return input
}

// NewOPAInputWithResourceAttributes creates OPA input with resource attributes
func NewOPAInputWithResourceAttributes(userID, email, tenantID, resourceType, resourceID, action string, roles []string, attributes map[string]interface{}) OPAAuthInput {
	input := NewOPAInput(userID, email, tenantID, resourceType, resourceID, action, roles)
	input.Resource.Attributes = attributes
	return input
}

// SetMFAVerified sets MFA verification status on input
func (i *OPAAuthInput) SetMFAVerified(verified bool) *OPAAuthInput {
	i.Context.MFAVerified = verified
	return i
}

// SetIPAddress sets IP address on input
func (i *OPAAuthInput) SetIPAddress(ip string) *OPAAuthInput {
	i.Context.IPAddress = ip
	return i
}

// SetSessionAge sets session age on input
func (i *OPAAuthInput) SetSessionAge(ageSeconds int) *OPAAuthInput {
	i.Context.SessionAge = ageSeconds
	return i
}

// SetUserMetadata sets user metadata on input
func (i *OPAAuthInput) SetUserMetadata(metadata map[string]interface{}) *OPAAuthInput {
	i.User.Metadata = metadata
	return i
}

// SetResourceAttributes sets resource attributes on input
func (i *OPAAuthInput) SetResourceAttributes(attributes map[string]interface{}) *OPAAuthInput {
	i.Resource.Attributes = attributes
	return i
}

// SetResourceOwner sets resource owner on input
func (i *OPAAuthInput) SetResourceOwner(ownerID string) *OPAAuthInput {
	i.Resource.OwnerID = ownerID
	return i
}

// SetTenantStatus sets tenant status on input
func (i *OPAAuthInput) SetTenantStatus(status string) *OPAAuthInput {
	i.Tenant.Status = status
	return i
}

// SetUserPermissions sets user permissions on input
func (i *OPAAuthInput) SetUserPermissions(permissions []string) *OPAAuthInput {
	i.User.Permissions = permissions
	return i
}
