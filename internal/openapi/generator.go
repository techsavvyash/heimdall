package openapi

import (
	"reflect"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/techsavvyash/heimdall/internal/service"
)

// Generator handles OpenAPI specification generation
type Generator struct {
	spec *openapi3.T
}

// NewGenerator creates a new OpenAPI generator
func NewGenerator() *Generator {
	return &Generator{
		spec: &openapi3.T{
			OpenAPI: "3.0.3",
			Info: &openapi3.Info{
				Title:       "Heimdall Authentication API",
				Description: "Heimdall is a comprehensive authentication service that acts as a proxy for FusionAuth, providing a unified authentication layer with OAuth 2.0 and OpenID Connect compliance.\n\n## Features\n- Email/Password Authentication\n- User Management\n- Role-Based Access Control (RBAC)\n- Multi-Tenancy\n- JWT-based Authentication\n\n## Authentication\nMost endpoints require a Bearer token in the Authorization header:\n```\nAuthorization: Bearer <access_token>\n```",
				Version:     "1.0.0",
				Contact: &openapi3.Contact{
					Name: "Heimdall API Support",
					URL:  "https://heimdall.yourdomain.com/support",
				},
				License: &openapi3.License{
					Name: "MIT",
					URL:  "https://opensource.org/licenses/MIT",
				},
			},
			Servers: openapi3.Servers{
				{
					URL:         "http://localhost:8080/v1",
					Description: "Local development server",
				},
				{
					URL:         "https://api-staging.heimdall.yourdomain.com/v1",
					Description: "Staging server",
				},
				{
					URL:         "https://api.heimdall.yourdomain.com/v1",
					Description: "Production server",
				},
			},
			Components: &openapi3.Components{
				SecuritySchemes: openapi3.SecuritySchemes{
					"bearerAuth": &openapi3.SecuritySchemeRef{
						Value: &openapi3.SecurityScheme{
							Type:         "http",
							Scheme:       "bearer",
							BearerFormat: "JWT",
							Description:  "JWT Bearer token authentication",
						},
					},
				},
				Schemas: make(openapi3.Schemas),
			},
			Paths: &openapi3.Paths{},
			Tags: openapi3.Tags{
				{Name: "Authentication", Description: "Authentication endpoints (login, register, etc.)"},
				{Name: "User Management", Description: "User CRUD operations"},
				{Name: "Tenants", Description: "Multi-tenant management"},
				{Name: "Password", Description: "Password management operations"},
				{Name: "Health", Description: "Health check endpoints"},
			},
		},
	}
}

// GenerateSpec generates the complete OpenAPI specification
func (g *Generator) GenerateSpec() *openapi3.T {
	// Register all schema components
	g.registerSchemas()

	// Add all API paths
	g.addAuthPaths()
	g.addUserPaths()
	g.addTenantPaths()
	g.addPasswordPaths()
	g.addHealthPath()

	return g.spec
}

// registerSchemas registers all schema components
func (g *Generator) registerSchemas() {
	// Request schemas
	g.addSchemaFromType("RegisterRequest", service.RegisterRequest{})
	g.addSchemaFromType("LoginRequest", service.LoginRequest{})
	g.addSchemaFromType("UpdateProfileRequest", service.UpdateProfileRequest{})
	g.addSchemaFromType("CreateTenantRequest", service.CreateTenantRequest{})
	g.addSchemaFromType("UpdateTenantRequest", service.UpdateTenantRequest{})
	g.addSchemaFromType("ChangePasswordRequest", service.ChangePasswordRequest{})

	// Response schemas
	g.addSchemaFromType("AuthResponse", service.AuthResponse{})
	g.addSchemaFromType("UserProfile", service.UserProfile{})
	g.addSchemaFromType("TenantResponse", service.TenantResponse{})

	// Add standard response wrappers
	g.addStandardResponseSchemas()

	// Add error schema
	g.addErrorSchema()
}

// addSchemaFromType creates a schema from a Go type using reflection
func (g *Generator) addSchemaFromType(name string, obj interface{}) {
	schema := g.reflectTypeToSchema(reflect.TypeOf(obj))
	g.spec.Components.Schemas[name] = &openapi3.SchemaRef{Value: schema}
}

// reflectTypeToSchema converts a Go type to OpenAPI schema using reflection
func (g *Generator) reflectTypeToSchema(t reflect.Type) *openapi3.Schema {
	// Handle pointer types
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	schema := &openapi3.Schema{
		Type:       &openapi3.Types{"object"},
		Properties: make(openapi3.Schemas),
	}

	// Process struct fields
	if t.Kind() == reflect.Struct {
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)

			// Skip unexported fields
			if !field.IsExported() {
				continue
			}

			// Get JSON tag
			jsonTag := field.Tag.Get("json")
			if jsonTag == "" || jsonTag == "-" {
				continue
			}

			// Parse JSON tag
			parts := strings.Split(jsonTag, ",")
			fieldName := parts[0]

			// Check for omitempty
			omitempty := false
			for _, part := range parts[1:] {
				if part == "omitempty" {
					omitempty = true
					break
				}
			}

			// Create field schema
			fieldSchema := g.createFieldSchema(field)

			// Add validation constraints from validate tag
			validateTag := field.Tag.Get("validate")
			g.applyValidationConstraints(fieldSchema, validateTag)

			// Add example from example tag
			exampleTag := field.Tag.Get("example")
			if exampleTag != "" {
				fieldSchema.Example = exampleTag
			}

			schema.Properties[fieldName] = &openapi3.SchemaRef{Value: fieldSchema}

			// Add to required if not omitempty
			if !omitempty && !strings.Contains(validateTag, "omitempty") {
				schema.Required = append(schema.Required, fieldName)
			}
		}
	}

	return schema
}

// createFieldSchema creates a schema for a struct field
func (g *Generator) createFieldSchema(field reflect.StructField) *openapi3.Schema {
	fieldType := field.Type

	// Handle pointer types
	if fieldType.Kind() == reflect.Ptr {
		fieldType = fieldType.Elem()
	}

	schema := &openapi3.Schema{}

	switch fieldType.Kind() {
	case reflect.String:
		schema.Type = &openapi3.Types{"string"}
		// Check for format hints in tags or field name
		fieldNameLower := strings.ToLower(field.Name)
		if strings.Contains(fieldNameLower, "email") {
			schema.Format = "email"
		} else if strings.Contains(fieldNameLower, "password") {
			schema.Format = "password"
		} else if strings.Contains(fieldNameLower, "id") && !strings.Contains(fieldNameLower, "tenantid") {
			schema.Format = "uuid"
		}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		schema.Type = &openapi3.Types{"integer"}
		if fieldType.Kind() == reflect.Int64 {
			schema.Format = "int64"
		} else {
			schema.Format = "int32"
		}

	case reflect.Bool:
		schema.Type = &openapi3.Types{"boolean"}

	case reflect.Float32, reflect.Float64:
		schema.Type = &openapi3.Types{"number"}
		if fieldType.Kind() == reflect.Float64 {
			schema.Format = "double"
		} else {
			schema.Format = "float"
		}

	case reflect.Slice, reflect.Array:
		schema.Type = &openapi3.Types{"array"}
		elemType := fieldType.Elem()
		elemSchema := g.createFieldSchemaFromType(elemType)
		schema.Items = &openapi3.SchemaRef{Value: elemSchema}

	case reflect.Map:
		schema.Type = &openapi3.Types{"object"}
		schema.AdditionalProperties = openapi3.AdditionalProperties{Has: boolPtr(true)}

	case reflect.Struct:
		// Handle time.Time
		if fieldType == reflect.TypeOf(time.Time{}) {
			schema.Type = &openapi3.Types{"string"}
			schema.Format = "date-time"
		} else {
			// For other structs, use object type
			schema.Type = &openapi3.Types{"object"}
		}

	default:
		schema.Type = &openapi3.Types{"string"}
	}

	return schema
}

// createFieldSchemaFromType creates a schema from a reflect.Type
func (g *Generator) createFieldSchemaFromType(t reflect.Type) *openapi3.Schema {
	schema := &openapi3.Schema{}

	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.String:
		schema.Type = &openapi3.Types{"string"}
	case reflect.Int, reflect.Int32, reflect.Int64:
		schema.Type = &openapi3.Types{"integer"}
	case reflect.Bool:
		schema.Type = &openapi3.Types{"boolean"}
	default:
		schema.Type = &openapi3.Types{"string"}
	}

	return schema
}

// applyValidationConstraints applies validation constraints from validate tag
func (g *Generator) applyValidationConstraints(schema *openapi3.Schema, validateTag string) {
	if validateTag == "" {
		return
	}

	rules := strings.Split(validateTag, ",")
	for _, rule := range rules {
		parts := strings.Split(rule, "=")
		ruleName := parts[0]

		switch ruleName {
		case "required":
			// Handled separately in reflectTypeToSchema
		case "email":
			schema.Format = "email"
		case "min":
			if len(parts) > 1 {
				if schema.Type != nil && (*schema.Type)[0] == "string" {
					minLen := parseInt(parts[1])
					schema.MinLength = uint64(minLen)
				}
			}
		case "max":
			if len(parts) > 1 {
				if schema.Type != nil && (*schema.Type)[0] == "string" {
					maxLen := parseInt(parts[1])
					schema.MaxLength = uint64Ptr(uint64(maxLen))
				}
			}
		}
	}
}

// addStandardResponseSchemas adds standard response wrapper schemas
func (g *Generator) addStandardResponseSchemas() {
	// Success response wrapper
	g.spec.Components.Schemas["SuccessResponse"] = &openapi3.SchemaRef{
		Value: &openapi3.Schema{
			Type: &openapi3.Types{"object"},
			Properties: openapi3.Schemas{
				"success": {
					Value: &openapi3.Schema{
						Type:    &openapi3.Types{"boolean"},
						Example: true,
					},
				},
				"data": {
					Value: &openapi3.Schema{
						Type: &openapi3.Types{"object"},
					},
				},
			},
		},
	}

	// Message response
	g.spec.Components.Schemas["MessageResponse"] = &openapi3.SchemaRef{
		Value: &openapi3.Schema{
			Type: &openapi3.Types{"object"},
			Properties: openapi3.Schemas{
				"success": {
					Value: &openapi3.Schema{
						Type:    &openapi3.Types{"boolean"},
						Example: true,
					},
				},
				"message": {
					Value: &openapi3.Schema{
						Type:    &openapi3.Types{"string"},
						Example: "Operation completed successfully",
					},
				},
			},
		},
	}
}

// addErrorSchema adds error response schema
func (g *Generator) addErrorSchema() {
	g.spec.Components.Schemas["Error"] = &openapi3.SchemaRef{
		Value: &openapi3.Schema{
			Type: &openapi3.Types{"object"},
			Properties: openapi3.Schemas{
				"success": {
					Value: &openapi3.Schema{
						Type:    &openapi3.Types{"boolean"},
						Example: false,
					},
				},
				"error": {
					Value: &openapi3.Schema{
						Type: &openapi3.Types{"object"},
						Properties: openapi3.Schemas{
							"code": {
								Value: &openapi3.Schema{
									Type:    &openapi3.Types{"string"},
									Example: "VALIDATION_ERROR",
								},
							},
							"message": {
								Value: &openapi3.Schema{
									Type:    &openapi3.Types{"string"},
									Example: "Invalid input parameters",
								},
							},
						},
					},
				},
			},
		},
	}
}

// Utility functions

func boolPtr(b bool) *bool {
	return &b
}

func uint64Ptr(u uint64) *uint64 {
	return &u
}

func parseInt(s string) int {
	// Simple integer parsing
	var result int
	for _, c := range s {
		if c >= '0' && c <= '9' {
			result = result*10 + int(c-'0')
		}
	}
	return result
}
