package openapi

import (
	"github.com/getkin/kin-openapi/openapi3"
)

// addAuthPaths adds authentication-related paths
func (g *Generator) addAuthPaths() {
	// POST /auth/register
	g.spec.Paths.Set("/auth/register", &openapi3.PathItem{
		Post: &openapi3.Operation{
			Tags:        []string{"Authentication"},
			Summary:     "Register a new user",
			Description: "Create a new user account with email and password",
			OperationID: "registerUser",
			RequestBody: &openapi3.RequestBodyRef{
				Value: &openapi3.RequestBody{
					Required:    true,
					Description: "User registration details",
					Content: openapi3.Content{
						"application/json": {
							Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/RegisterRequest"},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(201, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("User created successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/AuthResponse"},
							},
						},
					},
				}),
				openapi3.WithStatus(400, g.errorResponse("Invalid input")),
				openapi3.WithStatus(409, g.errorResponse("Email already exists")),
			),
		},
	})

	// POST /auth/login
	g.spec.Paths.Set("/auth/login", &openapi3.PathItem{
		Post: &openapi3.Operation{
			Tags:        []string{"Authentication"},
			Summary:     "Login with email and password",
			Description: "Authenticate user and return access tokens",
			OperationID: "login",
			RequestBody: &openapi3.RequestBodyRef{
				Value: &openapi3.RequestBody{
					Required:    true,
					Description: "Login credentials",
					Content: openapi3.Content{
						"application/json": {
							Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/LoginRequest"},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Login successful"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/AuthResponse"},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Invalid credentials")),
			),
		},
	})

	// POST /auth/refresh
	g.spec.Paths.Set("/auth/refresh", &openapi3.PathItem{
		Post: &openapi3.Operation{
			Tags:        []string{"Authentication"},
			Summary:     "Refresh access token",
			Description: "Obtain a new access token using refresh token",
			OperationID: "refreshToken",
			RequestBody: &openapi3.RequestBodyRef{
				Value: &openapi3.RequestBody{
					Required:    true,
					Description: "Refresh token",
					Content: openapi3.Content{
						"application/json": {
							Schema: &openapi3.SchemaRef{
								Value: &openapi3.Schema{
									Type: &openapi3.Types{"object"},
									Properties: openapi3.Schemas{
										"refreshToken": {
											Value: &openapi3.Schema{
												Type: &openapi3.Types{"string"},
											},
										},
									},
									Required: []string{"refreshToken"},
								},
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Token refreshed successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/AuthResponse"},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Invalid refresh token")),
			),
		},
	})

	// POST /auth/logout
	g.spec.Paths.Set("/auth/logout", &openapi3.PathItem{
		Post: &openapi3.Operation{
			Tags:        []string{"Authentication"},
			Summary:     "Logout user",
			Description: "Invalidate current session and tokens",
			OperationID: "logout",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Logout successful"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/MessageResponse"},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
			),
		},
	})

	// POST /auth/logout-all
	g.spec.Paths.Set("/auth/logout-all", &openapi3.PathItem{
		Post: &openapi3.Operation{
			Tags:        []string{"Authentication"},
			Summary:     "Logout from all devices",
			Description: "Invalidate all sessions and tokens for the user",
			OperationID: "logoutAll",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Logged out from all devices"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/MessageResponse"},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
			),
		},
	})
}

// addUserPaths adds user management paths
func (g *Generator) addUserPaths() {
	// GET /users/me
	g.spec.Paths.Set("/users/me", &openapi3.PathItem{
		Get: &openapi3.Operation{
			Tags:        []string{"User Management"},
			Summary:     "Get current user profile",
			Description: "Get authenticated user's profile information",
			OperationID: "getCurrentUser",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("User profile retrieved"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/UserProfile"},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
			),
		},
		Patch: &openapi3.Operation{
			Tags:        []string{"User Management"},
			Summary:     "Update current user profile",
			Description: "Update authenticated user's profile information",
			OperationID: "updateCurrentUser",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			RequestBody: &openapi3.RequestBodyRef{
				Value: &openapi3.RequestBody{
					Required:    true,
					Description: "Profile update data",
					Content: openapi3.Content{
						"application/json": {
							Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/UpdateProfileRequest"},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Profile updated successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/UserProfile"},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
				openapi3.WithStatus(400, g.errorResponse("Invalid input")),
			),
		},
	})

	// GET /users/:userId
	g.spec.Paths.Set("/users/{userId}", &openapi3.PathItem{
		Get: &openapi3.Operation{
			Tags:        []string{"User Management"},
			Summary:     "Get user by ID",
			Description: "Get specific user's profile (admin only)",
			OperationID: "getUserById",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			Parameters: openapi3.Parameters{
				{
					Value: &openapi3.Parameter{
						Name:        "userId",
						In:          "path",
						Required:    true,
						Description: "User ID",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type:   &openapi3.Types{"string"},
								Format: "uuid",
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("User retrieved successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/UserProfile"},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
				openapi3.WithStatus(403, g.errorResponse("Forbidden")),
				openapi3.WithStatus(404, g.errorResponse("User not found")),
			),
		},
		Delete: &openapi3.Operation{
			Tags:        []string{"User Management"},
			Summary:     "Delete user",
			Description: "Delete a user account (admin only)",
			OperationID: "deleteUser",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			Parameters: openapi3.Parameters{
				{
					Value: &openapi3.Parameter{
						Name:        "userId",
						In:          "path",
						Required:    true,
						Description: "User ID",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type:   &openapi3.Types{"string"},
								Format: "uuid",
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("User deleted successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/MessageResponse"},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
				openapi3.WithStatus(403, g.errorResponse("Forbidden")),
				openapi3.WithStatus(404, g.errorResponse("User not found")),
			),
		},
	})

	// POST /users/:userId/roles
	g.spec.Paths.Set("/users/{userId}/roles", &openapi3.PathItem{
		Post: &openapi3.Operation{
			Tags:        []string{"User Management"},
			Summary:     "Assign role to user",
			Description: "Assign a role to a user (admin only)",
			OperationID: "assignRoleToUser",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			Parameters: openapi3.Parameters{
				{
					Value: &openapi3.Parameter{
						Name:        "userId",
						In:          "path",
						Required:    true,
						Description: "User ID",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type:   &openapi3.Types{"string"},
								Format: "uuid",
							},
						},
					},
				},
			},
			RequestBody: &openapi3.RequestBodyRef{
				Value: &openapi3.RequestBody{
					Required: true,
					Content: openapi3.Content{
						"application/json": {
							Schema: &openapi3.SchemaRef{
								Value: &openapi3.Schema{
									Type: &openapi3.Types{"object"},
									Properties: openapi3.Schemas{
										"roleId": {
											Value: &openapi3.Schema{
												Type:   &openapi3.Types{"string"},
												Format: "uuid",
											},
										},
									},
									Required: []string{"roleId"},
								},
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Role assigned successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/MessageResponse"},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
				openapi3.WithStatus(403, g.errorResponse("Forbidden")),
			),
		},
	})

	// DELETE /users/:userId/roles/:roleId
	g.spec.Paths.Set("/users/{userId}/roles/{roleId}", &openapi3.PathItem{
		Delete: &openapi3.Operation{
			Tags:        []string{"User Management"},
			Summary:     "Remove role from user",
			Description: "Remove a role from a user (admin only)",
			OperationID: "removeRoleFromUser",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			Parameters: openapi3.Parameters{
				{
					Value: &openapi3.Parameter{
						Name:        "userId",
						In:          "path",
						Required:    true,
						Description: "User ID",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type:   &openapi3.Types{"string"},
								Format: "uuid",
							},
						},
					},
				},
				{
					Value: &openapi3.Parameter{
						Name:        "roleId",
						In:          "path",
						Required:    true,
						Description: "Role ID",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type:   &openapi3.Types{"string"},
								Format: "uuid",
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Role removed successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/MessageResponse"},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
				openapi3.WithStatus(403, g.errorResponse("Forbidden")),
			),
		},
	})
}

// addTenantPaths adds tenant management paths
func (g *Generator) addTenantPaths() {
	// GET /tenants
	g.spec.Paths.Set("/tenants", &openapi3.PathItem{
		Get: &openapi3.Operation{
			Tags:        []string{"Tenants"},
			Summary:     "List tenants",
			Description: "Get all tenants (admin only)",
			OperationID: "listTenants",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			Parameters: openapi3.Parameters{
				{
					Value: &openapi3.Parameter{
						Name:        "page",
						In:          "query",
						Description: "Page number",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type:    &openapi3.Types{"integer"},
								Default: 1,
								Min:     float64Ptr(1),
							},
						},
					},
				},
				{
					Value: &openapi3.Parameter{
						Name:        "pageSize",
						In:          "query",
						Description: "Items per page",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type:    &openapi3.Types{"integer"},
								Default: 20,
								Min:     float64Ptr(1),
								Max:     float64Ptr(100),
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Tenants retrieved successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{
									Value: &openapi3.Schema{
										Type: &openapi3.Types{"array"},
										Items: &openapi3.SchemaRef{
											Ref: "#/components/schemas/TenantResponse",
										},
									},
								},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
				openapi3.WithStatus(403, g.errorResponse("Forbidden")),
			),
		},
		Post: &openapi3.Operation{
			Tags:        []string{"Tenants"},
			Summary:     "Create tenant",
			Description: "Create a new tenant (admin only)",
			OperationID: "createTenant",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			RequestBody: &openapi3.RequestBodyRef{
				Value: &openapi3.RequestBody{
					Required: true,
					Content: openapi3.Content{
						"application/json": {
							Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/CreateTenantRequest"},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(201, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Tenant created successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/TenantResponse"},
							},
						},
					},
				}),
				openapi3.WithStatus(400, g.errorResponse("Invalid input")),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
				openapi3.WithStatus(403, g.errorResponse("Forbidden")),
			),
		},
	})

	// GET /tenants/:tenantId
	g.spec.Paths.Set("/tenants/{tenantId}", &openapi3.PathItem{
		Get: &openapi3.Operation{
			Tags:        []string{"Tenants"},
			Summary:     "Get tenant by ID",
			Description: "Get specific tenant details",
			OperationID: "getTenantById",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			Parameters: openapi3.Parameters{
				{
					Value: &openapi3.Parameter{
						Name:        "tenantId",
						In:          "path",
						Required:    true,
						Description: "Tenant ID",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type:   &openapi3.Types{"string"},
								Format: "uuid",
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Tenant retrieved successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/TenantResponse"},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
				openapi3.WithStatus(404, g.errorResponse("Tenant not found")),
			),
		},
		Patch: &openapi3.Operation{
			Tags:        []string{"Tenants"},
			Summary:     "Update tenant",
			Description: "Update tenant details (admin only)",
			OperationID: "updateTenant",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			Parameters: openapi3.Parameters{
				{
					Value: &openapi3.Parameter{
						Name:        "tenantId",
						In:          "path",
						Required:    true,
						Description: "Tenant ID",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type:   &openapi3.Types{"string"},
								Format: "uuid",
							},
						},
					},
				},
			},
			RequestBody: &openapi3.RequestBodyRef{
				Value: &openapi3.RequestBody{
					Required: true,
					Content: openapi3.Content{
						"application/json": {
							Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/UpdateTenantRequest"},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Tenant updated successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/TenantResponse"},
							},
						},
					},
				}),
				openapi3.WithStatus(400, g.errorResponse("Invalid input")),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
				openapi3.WithStatus(403, g.errorResponse("Forbidden")),
				openapi3.WithStatus(404, g.errorResponse("Tenant not found")),
			),
		},
		Delete: &openapi3.Operation{
			Tags:        []string{"Tenants"},
			Summary:     "Delete tenant",
			Description: "Delete a tenant (admin only)",
			OperationID: "deleteTenant",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			Parameters: openapi3.Parameters{
				{
					Value: &openapi3.Parameter{
						Name:        "tenantId",
						In:          "path",
						Required:    true,
						Description: "Tenant ID",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type:   &openapi3.Types{"string"},
								Format: "uuid",
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Tenant deleted successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/MessageResponse"},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
				openapi3.WithStatus(403, g.errorResponse("Forbidden")),
				openapi3.WithStatus(404, g.errorResponse("Tenant not found")),
			),
		},
	})

	// GET /tenants/slug/:slug
	g.spec.Paths.Set("/tenants/slug/{slug}", &openapi3.PathItem{
		Get: &openapi3.Operation{
			Tags:        []string{"Tenants"},
			Summary:     "Get tenant by slug",
			Description: "Get tenant details by slug",
			OperationID: "getTenantBySlug",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			Parameters: openapi3.Parameters{
				{
					Value: &openapi3.Parameter{
						Name:        "slug",
						In:          "path",
						Required:    true,
						Description: "Tenant slug",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type: &openapi3.Types{"string"},
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Tenant retrieved successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/TenantResponse"},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
				openapi3.WithStatus(404, g.errorResponse("Tenant not found")),
			),
		},
	})

	// GET /tenants/:tenantId/stats
	g.spec.Paths.Set("/tenants/{tenantId}/stats", &openapi3.PathItem{
		Get: &openapi3.Operation{
			Tags:        []string{"Tenants"},
			Summary:     "Get tenant statistics",
			Description: "Get statistics for a tenant",
			OperationID: "getTenantStats",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			Parameters: openapi3.Parameters{
				{
					Value: &openapi3.Parameter{
						Name:        "tenantId",
						In:          "path",
						Required:    true,
						Description: "Tenant ID",
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type:   &openapi3.Types{"string"},
								Format: "uuid",
							},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Statistics retrieved successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{
									Value: &openapi3.Schema{
										Type: &openapi3.Types{"object"},
										AdditionalProperties: openapi3.AdditionalProperties{
											Has: boolPtr(true),
										},
									},
								},
							},
						},
					},
				}),
				openapi3.WithStatus(401, g.errorResponse("Unauthorized")),
				openapi3.WithStatus(404, g.errorResponse("Tenant not found")),
			),
		},
	})
}

// addPasswordPaths adds password management paths
func (g *Generator) addPasswordPaths() {
	// POST /auth/password/change
	g.spec.Paths.Set("/auth/password/change", &openapi3.PathItem{
		Post: &openapi3.Operation{
			Tags:        []string{"Password"},
			Summary:     "Change password",
			Description: "Change password for authenticated user",
			OperationID: "changePassword",
			Security:    &openapi3.SecurityRequirements{{"bearerAuth": {}}},
			RequestBody: &openapi3.RequestBodyRef{
				Value: &openapi3.RequestBody{
					Required: true,
					Content: openapi3.Content{
						"application/json": {
							Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/ChangePasswordRequest"},
						},
					},
				},
			},
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Password changed successfully"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/MessageResponse"},
							},
						},
					},
				}),
				openapi3.WithStatus(400, g.errorResponse("Invalid input")),
				openapi3.WithStatus(401, g.errorResponse("Invalid current password")),
			),
		},
	})
}

// addHealthPath adds health check path
func (g *Generator) addHealthPath() {
	g.spec.Paths.Set("/health", &openapi3.PathItem{
		Get: &openapi3.Operation{
			Tags:        []string{"Health"},
			Summary:     "Health check",
			Description: "Basic health check endpoint",
			OperationID: "healthCheck",
			Responses: openapi3.NewResponses(
				openapi3.WithStatus(200, &openapi3.ResponseRef{
					Value: &openapi3.Response{
						Description: stringPtr("Service is healthy"),
						Content: openapi3.Content{
							"application/json": {
								Schema: &openapi3.SchemaRef{
									Value: &openapi3.Schema{
										Type: &openapi3.Types{"object"},
										Properties: openapi3.Schemas{
											"status": {
												Value: &openapi3.Schema{
													Type:    &openapi3.Types{"string"},
													Example: "healthy",
												},
											},
											"timestamp": {
												Value: &openapi3.Schema{
													Type:   &openapi3.Types{"string"},
													Format: "date-time",
												},
											},
										},
									},
								},
							},
						},
					},
				}),
			),
		},
	})
}

// errorResponse creates a standard error response
func (g *Generator) errorResponse(description string) *openapi3.ResponseRef {
	return &openapi3.ResponseRef{
		Value: &openapi3.Response{
			Description: stringPtr(description),
			Content: openapi3.Content{
				"application/json": {
					Schema: &openapi3.SchemaRef{Ref: "#/components/schemas/Error"},
				},
			},
		},
	}
}

// Utility functions
func stringPtr(s string) *string {
	return &s
}

func float64Ptr(f float64) *float64 {
	return &f
}
