# Testing Guide for Heimdall

This document provides information about testing Heimdall.

## Prerequisites

Before running tests, ensure you have:

1. **PostgreSQL** running on localhost:5432
2. **Test Database** created:
   ```bash
   createdb heimdall_test
   ```
3. **Database User** with permissions:
   ```sql
   CREATE USER heimdall WITH PASSWORD 'heimdall_password';
   GRANT ALL PRIVILEGES ON DATABASE heimdall_test TO heimdall;
   ```

## Running Tests

### Run All Tests
```bash
make test
```

### Run Tests with Coverage
```bash
make test-coverage
```

This will generate a `coverage.html` file that you can open in your browser.

### Run Specific Tests
```bash
# Run JWT service tests
go test -v ./internal/auth -run TestJWT

# Run tenant service tests
go test -v ./internal/service -run TestTenant

# Run tenant API tests
go test -v ./internal/api -run TestTenantHandler
```

### Run Tests with Race Detection
```bash
go test -race ./...
```

## Test Structure

### Unit Tests
Located alongside the code they test:
- `internal/auth/jwt_test.go` - JWT service tests
- `internal/service/tenant_service_test.go` - Tenant service tests

### Integration Tests
Located in the API package:
- `internal/api/tenant_handler_test.go` - Tenant endpoint tests

### Test Utilities
Common test helpers in `internal/testutil/`:
- `database.go` - Database setup and test data helpers
- `jwt.go` - JWT token generation for tests
- `http.go` - HTTP request helpers and assertions

## Writing Tests

### Unit Test Example
```go
func TestTenantService_CreateTenant(t *testing.T) {
    testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
        testutil.TruncateTables(t, db)

        tenantService := service.NewTenantService(db)
        ctx := testutil.CreateTestContext(t)

        req := &service.CreateTenantRequest{
            Name: "Test Corp",
            Slug: "test-corp",
        }

        tenant, err := tenantService.CreateTenant(ctx, req)
        if err != nil {
            t.Fatalf("Failed to create tenant: %v", err)
        }

        if tenant.Name != req.Name {
            t.Errorf("Expected name '%s', got '%s'", req.Name, tenant.Name)
        }
    })
}
```

### Integration Test Example
```go
func TestTenantHandler_CreateTenant(t *testing.T) {
    testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
        testutil.TruncateTables(t, db)

        app, token := setupTenantTestApp(t, db)

        body := map[string]interface{}{
            "name": "Test Corporation",
            "slug": "test-corp",
        }

        resp := testutil.MakeRequest(t, app, "POST", "/v1/tenants", body,
            testutil.WithAuthHeader(token))

        testutil.AssertStatusCode(t, http.StatusCreated, resp.Code)

        jsonResp := testutil.ParseJSONResponse(t, resp)
        testutil.AssertJSONSuccess(t, jsonResp)
    })
}
```

## Test Utilities

### Database Helpers
```go
// Setup test database
db := testutil.SetupTestDB(t)
defer testutil.CleanupTestDB(t, db)

// Truncate all tables (faster than recreating)
testutil.TruncateTables(t, db)

// Create test data
tenant := testutil.CreateTestTenant(t, db, "Test Tenant", "test-slug")
user := testutil.CreateTestUser(t, db, tenant, "user@example.com")
role := testutil.CreateTestRole(t, db, tenant, "Admin")
permission := testutil.CreateTestPermission(t, db, "users.read", "users", "read")

// Assign relationships
testutil.AssignRoleToUser(t, db, user, role)
testutil.AssignPermissionToRole(t, db, role, permission)
```

### JWT Helpers
```go
// Create JWT service
jwtService, cleanup := testutil.CreateTestJWTService(t)
defer cleanup()

// Generate test token
token := testutil.GenerateTestToken(t, jwtService,
    "user-id", "tenant-id", "test@example.com", []string{"admin"})
```

### HTTP Helpers
```go
// Make HTTP request
resp := testutil.MakeRequest(t, app, "POST", "/v1/tenants", body,
    testutil.WithAuthHeader(token))

// Parse JSON response
jsonResp := testutil.ParseJSONResponse(t, resp)

// Assertions
testutil.AssertStatusCode(t, http.StatusOK, resp.Code)
testutil.AssertJSONSuccess(t, jsonResp)
testutil.AssertJSONError(t, jsonResp, "VALIDATION_ERROR")
testutil.AssertJSONField(t, jsonResp, "message", "Success")

// Get data field
data := testutil.GetDataField(t, jsonResp)
```

## Test Coverage

Current test coverage:
- ✅ JWT Service (token generation, validation, expiry)
- ✅ Tenant Service (CRUD, validation, stats)
- ✅ Tenant API Endpoints (all endpoints)
- 🚧 Auth Service (in progress)
- 🚧 User Service (in progress)
- 🚧 User API Endpoints (in progress)

## Continuous Integration

Tests are run automatically on:
- Every commit (via pre-commit hooks)
- Every pull request
- Before deployment

## Troubleshooting

### "Failed to connect to test database"
Ensure PostgreSQL is running and the test database exists:
```bash
psql -U postgres -c "CREATE DATABASE heimdall_test;"
```

### "Permission denied" errors
Grant permissions to the test user:
```bash
psql -U postgres -d heimdall_test -c "GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO heimdall;"
```

### Tests are slow
Use table truncation instead of recreation:
```go
testutil.TruncateTables(t, db)  // Fast
// instead of
testutil.CleanupTestDB(t, db)   // Slow
testutil.SetupTestDB(t)
```

### Flaky tests
If tests fail intermittently:
1. Check for race conditions: `go test -race`
2. Ensure proper cleanup between tests
3. Use transactions for database isolation

## Best Practices

1. **Test Isolation**: Each test should be independent
2. **Clean State**: Always truncate/clean database between tests
3. **Descriptive Names**: Use clear test function names
4. **Table-Driven Tests**: Use test tables for multiple scenarios
5. **Error Checking**: Always check and report errors properly
6. **Cleanup**: Use `defer` for cleanup operations
7. **Helpers**: Extract common setup into helper functions

## Resources

- [Go Testing Documentation](https://golang.org/pkg/testing/)
- [Testify Assertions](https://github.com/stretchr/testify)
- [GORM Testing](https://gorm.io/docs/testing.html)
- [Fiber Testing](https://docs.gofiber.io/guide/testing)
