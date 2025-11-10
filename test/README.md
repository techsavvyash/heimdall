# Heimdall Integration Tests

This directory contains integration tests for the Heimdall authentication and authorization service.

## Test Structure

```
test/
├── integration/        # Integration test suites
│   └── auth_test.go   # Authentication flow tests
├── helpers/           # Test helper functions
│   └── auth.go        # Authentication helpers
├── utils/             # Test utilities
│   └── client.go      # HTTP client and assertions
├── run-integration-tests.sh  # Test runner script
└── README.md          # This file
```

## Prerequisites

Before running tests, ensure the following services are running:

1. **Heimdall API** - The main API server
2. **PostgreSQL** - Database for storing user data
3. **Redis** - Cache for tokens and sessions
4. **FusionAuth** - External authentication provider
5. **OPA** - Open Policy Agent for authorization
6. **MinIO** - Object storage for OPA bundles

Start all services with:

```bash
docker-compose up -d
```

## Running Tests

### Run All Integration Tests

```bash
# Using make
make test-integration

# Or directly with the script
./test/run-integration-tests.sh

# Or with Go
go test -v ./test/integration/... -timeout 5m
```

### Run Authentication Tests Only

```bash
# Using make
make test-auth

# Or with Go
go test -v ./test/integration -run TestUser -timeout 5m
```

### Run Specific Test

```bash
# Run a specific test function
go test -v ./test/integration -run TestUserRegistration -timeout 5m

# Run a specific sub-test
go test -v ./test/integration -run TestUserRegistration/Successful -timeout 5m
```

## Test Configuration

Tests can be configured using environment variables:

- `HEIMDALL_API_URL` - Base URL for the Heimdall API (default: `http://localhost:8080`)

Example:

```bash
HEIMDALL_API_URL=http://localhost:8080 go test -v ./test/integration/...
```

## Authentication Tests

The authentication test suite (`auth_test.go`) covers the following scenarios:

### User Registration (`TestUserRegistration`)
- ✅ Successful registration with valid data
- ✅ Registration fails with duplicate email
- ✅ Registration fails with invalid email
- ✅ Registration fails with weak password
- ✅ Registration fails with missing required fields

### User Login (`TestUserLogin`)
- ✅ Successful login with valid credentials
- ✅ Login fails with incorrect password
- ✅ Login fails with non-existent email
- ✅ Login fails with invalid email format

### Token Refresh (`TestTokenRefresh`)
- ✅ Successful token refresh with valid refresh token
- ✅ Token refresh fails with invalid refresh token
- ✅ Token refresh fails with empty refresh token

### Logout (`TestLogout`)
- ✅ Successful logout with valid token
- ✅ Logout fails without authentication

### Protected Endpoint Access (`TestProtectedEndpointAccess`)
- ✅ Access protected endpoint with valid token
- ✅ Access protected endpoint without token fails
- ✅ Access protected endpoint with invalid token fails

### Password Change (`TestPasswordChange`)
- ✅ Successful password change
- ✅ Password change fails with incorrect current password

## Test Utilities

### Test Client (`test/utils/client.go`)

Provides an HTTP client with helper methods for making requests:

```go
client := utils.NewTestClient("http://localhost:8080")
client.SetAuthToken(token)

resp, err := client.Request(http.MethodGet, "/v1/users/me", nil, nil)
```

### Assertions (`test/utils/client.go`)

Common assertion functions:

```go
utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode)
utils.AssertNoError(t, err)
utils.AssertEqual(t, expected, actual)
utils.AssertNotEmpty(t, value)
utils.AssertTrue(t, condition)
utils.AssertFalse(t, condition)
```

### Auth Helpers (`test/helpers/auth.go`)

Helper functions for authentication operations:

```go
// Register a user
authResp := helpers.RegisterUser(t, client, helpers.RegisterRequest{
    Email:     "test@example.com",
    Password:  "Test123456!",
    FirstName: "Test",
    LastName:  "User",
})

// Login a user
authResp := helpers.LoginUser(t, client, helpers.LoginRequest{
    Email:    "test@example.com",
    Password: "Test123456!",
})

// Refresh token
authResp := helpers.RefreshToken(t, client, refreshToken)

// Create test user with unique email
authResp := helpers.CreateTestUser(t, client, "prefix")

// Assert successful authentication
helpers.AssertAuthSuccess(t, authResp, "Registration")

// Assert authentication failure
helpers.AssertAuthFailure(t, authResp, "INVALID_CREDENTIALS", "Login")
```

## Writing New Tests

### Example Test Structure

```go
func TestNewFeature(t *testing.T) {
    // Create test user
    testUser := helpers.CreateTestUser(t, client, "feature-test")

    t.Run("Successful scenario", func(t *testing.T) {
        // Set auth token
        client.SetAuthToken(testUser.Data.AccessToken)

        // Make request
        resp, err := client.Request(http.MethodGet, "/v1/endpoint", nil, nil)
        utils.AssertNoError(t, err)
        utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode)

        // Decode and validate response
        var result MyResponse
        err = client.DecodeResponse(resp, &result)
        utils.AssertNoError(t, err)
        utils.AssertTrue(t, result.Success)

        // Clear auth token
        client.SetAuthToken("")
    })
}
```

## Continuous Integration

Tests are designed to run in CI/CD pipelines. Example GitHub Actions workflow:

```yaml
name: Integration Tests

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest

    steps:
      - uses: actions/checkout@v3

      - name: Start services
        run: docker-compose up -d

      - name: Wait for services
        run: sleep 30

      - name: Run tests
        run: make test-integration
```

## Troubleshooting

### Tests Fail with "API is not running"

Ensure all Docker services are started:

```bash
docker-compose up -d
docker-compose ps
```

Check API health:

```bash
curl http://localhost:8080/health
```

### Tests Fail with "Connection refused"

Check if services are listening on the correct ports:

```bash
lsof -i :8080  # Heimdall API
lsof -i :5432  # PostgreSQL
lsof -i :6379  # Redis
```

### Tests Timeout

Increase the timeout value:

```bash
go test -v ./test/integration/... -timeout 10m
```

### Database State Issues

Reset the database:

```bash
docker-compose down
docker-compose up -d
```

Or run migrations manually:

```bash
go run cmd/migrate/main.go fresh
```

## Best Practices

1. **Isolation**: Each test should be independent and not rely on others
2. **Cleanup**: Always clean up test data (clear auth tokens, etc.)
3. **Unique Data**: Use timestamps or random values to generate unique test data
4. **Error Handling**: Always check errors from helper functions
5. **Descriptive Names**: Use clear, descriptive test names
6. **Sub-tests**: Group related scenarios using `t.Run()`
7. **Assertions**: Use provided assertion helpers for consistent error messages

## Next Steps

- Add OPA authorization tests
- Add RBAC integration tests
- Add ABAC integration tests
- Add policy management tests
- Add tenant management tests
- Add performance/load tests
