# Heimdall OPA Integration - Manual Testing Guide

**Date:** November 10, 2025
**Branch:** feat/OPA
**Status:** Rego Syntax Fixed - Ready for Testing
**Last Updated:** After Rego syntax fix commit

---

## ⚠️ Prerequisites Complete

✅ **All Rego policy syntax has been fixed** (commit: fix: update Rego policies to modern OPA syntax)
✅ **199 rule bodies updated** across 7 policy files
✅ **Compatible with latest OPA versions**

---

## Table of Contents

1. [Environment Setup](#environment-setup)
2. [Start Services](#start-services)
3. [Verify Services Health](#verify-services-health)
4. [Load Policies into OPA](#load-policies-into-opa)
5. [Run Integration Tests](#run-integration-tests)
6. [Manual API Testing](#manual-api-testing)
7. [Testing Scenarios](#testing-scenarios)
8. [Expected Results](#expected-results)
9. [Troubleshooting](#troubleshooting)

---

## Environment Setup

### 1. Prerequisites

Ensure you have the following installed:
- Docker & Docker Compose
- Go 1.21 or higher
- curl
- jq (optional, for JSON formatting)

### 2. Clone and Checkout

```bash
cd /path/to/heimdall
git checkout feat/OPA
git pull origin feat/OPA
```

---

## Start Services

### 1. Start All Docker Services

```bash
# Start all services (Heimdall, OPA, PostgreSQL, Redis, FusionAuth, MinIO)
docker compose up -d

# Check service status
docker compose ps

# View logs
docker compose logs -f heimdall
docker compose logs -f opa
```

### 2. Wait for Services to Initialize

```bash
# Wait for API to be ready
while ! curl -sf http://localhost:8080/health > /dev/null; do
    echo "Waiting for Heimdall API..."
    sleep 2
done
echo "✅ Heimdall API is ready"

# Wait for OPA to be ready
while ! curl -sf http://localhost:8181/health > /dev/null; do
    echo "Waiting for OPA..."
    sleep 2
done
echo "✅ OPA is ready"

# Wait for FusionAuth
while ! curl -sf http://localhost:9011/api/status > /dev/null; do
    echo "Waiting for FusionAuth..."
    sleep 2
done
echo "✅ FusionAuth is ready"
```

---

## Verify Services Health

### 1. Check Heimdall API

```bash
curl http://localhost:8080/health
# Expected: {"status":"ok","timestamp":"..."}
```

### 2. Check OPA

```bash
curl http://localhost:8181/health
# Expected: {}  (200 OK)

curl http://localhost:8181/v1/data
# Expected: {"result":{}}
```

### 3. Check PostgreSQL

```bash
docker compose exec postgres psql -U heimdall -d heimdall -c "SELECT version();"
```

### 4. Check Redis

```bash
docker compose exec redis redis-cli ping
# Expected: PONG
```

### 5. Check MinIO

```bash
curl http://localhost:9001/minio/health/live
# Expected: 200 OK
```

---

## Load Policies into OPA

### 1. Use the Load Script

```bash
chmod +x load-policies.sh
./load-policies.sh
```

### 2. Manual Policy Loading

```bash
# Load each policy individually
curl -X PUT http://localhost:8181/v1/policies/authz \
  -H "Content-Type: text/plain" \
  --data-binary "@policies/authz.rego"

curl -X PUT http://localhost:8181/v1/policies/rbac \
  -H "Content-Type: text/plain" \
  --data-binary "@policies/rbac.rego"

curl -X PUT http://localhost:8181/v1/policies/abac \
  -H "Content-Type: text/plain" \
  --data-binary "@policies/abac.rego"

curl -X PUT http://localhost:8181/v1/policies/resource_ownership \
  -H "Content-Type: text/plain" \
  --data-binary "@policies/resource_ownership.rego"

curl -X PUT http://localhost:8181/v1/policies/time_based \
  -H "Content-Type: text/plain" \
  --data-binary "@policies/time_based.rego"

curl -X PUT http://localhost:8181/v1/policies/tenant_isolation \
  -H "Content-Type: text/plain" \
  --data-binary "@policies/tenant_isolation.rego"

curl -X PUT http://localhost:8181/v1/policies/helpers \
  -H "Content-Type: text/plain" \
  --data-binary "@policies/helpers.rego"
```

### 3. Verify Policies Loaded

```bash
curl http://localhost:8181/v1/policies | jq '.'

# Should show all 7 policies without parse errors
```

### 4. Test OPA Policy Decision

```bash
curl -X POST http://localhost:8181/v1/data/heimdall/authz \
  -H "Content-Type: application/json" \
  -d '{
    "input": {
      "user": {
        "id": "test-user",
        "roles": ["user"],
        "permissions": ["users.read"],
        "tenantId": "tenant-123"
      },
      "resource": {
        "type": "users",
        "id": "test-user",
        "tenantId": "tenant-123"
      },
      "action": "read",
      "tenant": {
        "id": "tenant-123",
        "status": "active"
      },
      "time": {
        "isBusinessHours": true,
        "isWeekend": false,
        "hour": 14
      },
      "context": {
        "ipAddress": "127.0.0.1",
        "mfaVerified": false
      }
    }
  }' | jq '.'
```

**Expected Result:**
```json
{
  "result": {
    "allow": true,
    "decision": true
  }
}
```

---

## Run Integration Tests

### 1. Run All OPA Integration Tests

```bash
cd /path/to/heimdall
go test -v ./test/integration -run TestOPA -timeout 5m
```

**Expected Output:**
```
=== RUN   TestOPARBACBasicPermissions
=== RUN   TestOPARBACBasicPermissions/Regular_user_cannot_list_all_users
=== RUN   TestOPARBACBasicPermissions/User_can_access_their_own_profile
=== RUN   TestOPARBACBasicPermissions/User_can_update_their_own_profile
--- PASS: TestOPARBACBasicPermissions (0.15s)

=== RUN   TestOPATenantIsolation
=== RUN   TestOPATenantIsolation/User_can_only_access_resources_in_their_tenant
=== RUN   TestOPATenantIsolation/User_permissions_are_retrieved_correctly
--- PASS: TestOPATenantIsolation (0.08s)

[... 6 more test suites ...]

PASS
ok      github.com/techsavvyash/heimdall/test/integration      2.156s
```

### 2. Run Specific Test Suites

```bash
# Test RBAC
go test -v ./test/integration -run TestOPARBACBasicPermissions

# Test tenant isolation
go test -v ./test/integration -run TestOPATenantIsolation

# Test protected endpoints
go test -v ./test/integration -run TestOPAProtectedEndpoints

# Test authentication
go test -v ./test/integration -run TestOPAAuthenticationRequired

# Test self-access
go test -v ./test/integration -run TestOPASelfAccessRules

# Test user management
go test -v ./test/integration -run TestOPAUserManagementPermissions

# Test token validation
go test -v ./test/integration -run TestOPATokenValidation

# Test session management
go test -v ./test/integration -run TestOPASessionManagement
```

### 3. Run with Coverage

```bash
go test -v ./test/integration -run TestOPA -coverprofile=coverage.out
go tool cover -html=coverage.out -o coverage.html
```

---

## Manual API Testing

### Step 1: Register a User

```bash
curl -X POST http://localhost:8080/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{
    "email": "testuser@example.com",
    "password": "SecurePass123!",
    "firstName": "Test",
    "lastName": "User"
  }' | jq '.'
```

**Expected Response:**
```json
{
  "success": true,
  "data": {
    "user": {
      "id": "...",
      "email": "testuser@example.com",
      "firstName": "Test",
      "lastName": "User"
    }
  }
}
```

### Step 2: Login

```bash
curl -X POST http://localhost:8080/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "testuser@example.com",
    "password": "SecurePass123!"
  }' | jq '.' > login-response.json

# Extract token
TOKEN=$(cat login-response.json | jq -r '.data.token')
echo "Token: $TOKEN"
```

### Step 3: Test Self-Access (Should Succeed)

```bash
# Get own user ID from login response
USER_ID=$(cat login-response.json | jq -r '.data.user.id')

# Access own profile (should work - self-access rule)
curl -X GET http://localhost:8080/v1/users/$USER_ID \
  -H "Authorization: Bearer $TOKEN" | jq '.'
```

**Expected:** 200 OK with user data

### Step 4: Test Unauthorized Access (Should Fail)

```bash
# Try to list all users without admin permission (should fail)
curl -X GET http://localhost:8080/v1/users \
  -H "Authorization: Bearer $TOKEN" \
  -v | jq '.'
```

**Expected:** 403 Forbidden
```json
{
  "success": false,
  "error": {
    "message": "Access denied: insufficient permissions",
    "code": "FORBIDDEN"
  }
}
```

### Step 5: Test Policy Endpoints (Should Fail for Regular User)

```bash
# Try to access policies (admin only)
curl -X GET http://localhost:8080/v1/policies \
  -H "Authorization: Bearer $TOKEN" \
  -v | jq '.'
```

**Expected:** 403 Forbidden

### Step 6: Create Admin User

```bash
# Register admin user
curl -X POST http://localhost:8080/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{
    "email": "admin@example.com",
    "password": "AdminPass123!",
    "firstName": "Admin",
    "lastName": "User"
  }' | jq '.'

# Login as admin
curl -X POST http://localhost:8080/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "admin@example.com",
    "password": "AdminPass123!"
  }' | jq '.' > admin-login.json

ADMIN_TOKEN=$(cat admin-login.json | jq -r '.data.token')
ADMIN_USER_ID=$(cat admin-login.json | jq -r '.data.user.id')

# Assign admin role (this might require direct database access or super admin)
# For now, document that admin role assignment needs to be done via database or super admin API
```

### Step 7: Test Admin Access

```bash
# List all users (should work for admin)
curl -X GET http://localhost:8080/v1/users \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq '.'

# Access policies (should work for admin)
curl -X GET http://localhost:8080/v1/policies \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq '.'
```

---

## Testing Scenarios

### Scenario 1: RBAC Permission Enforcement

**Test:** Regular user tries to access admin endpoint
**Expected:** 403 Forbidden
**Verification:**
```bash
curl -X GET http://localhost:8080/v1/users \
  -H "Authorization: Bearer $TOKEN" -w "\nHTTP Status: %{http_code}\n"
```

---

### Scenario 2: Tenant Isolation

**Test:** User from Tenant A tries to access resource from Tenant B
**Expected:** 403 Forbidden
**Manual Setup Required:**
1. Create two tenants
2. Create users in each tenant
3. Create resources in each tenant
4. Verify cross-tenant access is blocked

---

### Scenario 3: Self-Access Rules

**Test:** User accesses their own profile
**Expected:** 200 OK
```bash
curl -X GET http://localhost:8080/v1/users/$USER_ID \
  -H "Authorization: Bearer $TOKEN"
```

**Test:** User updates their own profile
**Expected:** 200 OK
```bash
curl -X PUT http://localhost:8080/v1/users/$USER_ID \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "firstName": "Updated",
    "lastName": "Name"
  }'
```

---

### Scenario 4: Time-Based Access

**Test:** Access during business hours vs after hours
**Note:** Currently time checks happen server-side based on system time
**Verification:** Check OPA logs for time-based policy evaluation

---

### Scenario 5: MFA Requirements

**Test:** Sensitive operation requires MFA
**Expected:** If MFA not verified, 403 Forbidden
**Example:** Assigning roles, creating policies, deploying bundles

---

### Scenario 6: Policy Management

**Prerequisites:** Admin user with `policies.*` permissions

```bash
# Create a new policy
curl -X POST http://localhost:8080/v1/policies \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test_policy",
    "description": "Test policy for manual testing",
    "content": "package test\n\ndefault allow = false\n\nallow if {\n    true\n}",
    "namespace": "test"
  }' | jq '.'

# List policies
curl -X GET http://localhost:8080/v1/policies \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq '.'

# Publish policy
POLICY_ID="<from-create-response>"
curl -X POST http://localhost:8080/v1/policies/$POLICY_ID/publish \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq '.'
```

---

### Scenario 7: Bundle Management

**Prerequisites:** Admin user with `bundles.*` permissions

```bash
# Create a bundle
curl -X POST http://localhost:8080/v1/bundles \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test_bundle",
    "description": "Test bundle for manual testing",
    "version": "1.0.0",
    "policies": []
  }' | jq '.'

# Activate bundle
BUNDLE_ID="<from-create-response>"
curl -X POST http://localhost:8080/v1/bundles/$BUNDLE_ID/activate \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq '.'
```

---

## Expected Results

### Integration Tests

**All 23 tests should pass:**
- ✅ TestOPARBACBasicPermissions (3 tests)
- ✅ TestOPATenantIsolation (2 tests)
- ✅ TestOPAProtectedEndpoints (3 tests)
- ✅ TestOPAAuthenticationRequired (3 tests)
- ✅ TestOPASelfAccessRules (4 tests)
- ✅ TestOPAUserManagementPermissions (3 tests)
- ✅ TestOPATokenValidation (3 tests)
- ✅ TestOPASessionManagement (2 tests)

**Total:** 23/23 passing

### Manual Tests

1. **Self-access:** Users can read/update own profile ✅
2. **Admin protection:** Regular users cannot access admin endpoints ✅
3. **Tenant isolation:** Cross-tenant access blocked ✅
4. **Authentication:** Unauthenticated requests rejected ✅
5. **Token validation:** Invalid tokens rejected ✅
6. **Policy management:** Admins can manage policies ✅
7. **Bundle management:** Admins can manage bundles ✅

---

## Troubleshooting

### Issue: OPA Policies Not Loading

**Symptom:** Policies return 500 errors or parse errors
**Solution:**
1. Check OPA logs: `docker compose logs opa`
2. Verify Rego syntax: All rules should have `if` keyword
3. Reload policies: `./load-policies.sh`

### Issue: 500 Internal Server Error on Protected Endpoints

**Symptom:** Getting 500 instead of 403
**Cause:** OPA policy evaluation failing
**Solution:**
1. Check Heimdall logs: `docker compose logs heimdall`
2. Check OPA connectivity: `curl http://localhost:8181/health`
3. Verify policies loaded: `curl http://localhost:8181/v1/policies`
4. Test policy evaluation manually (see section above)

### Issue: Integration Tests Fail

**Symptom:** Tests timeout or fail to connect
**Solution:**
1. Ensure all services running: `docker compose ps`
2. Check API health: `curl http://localhost:8080/health`
3. Check OPA health: `curl http://localhost:8181/health`
4. Restart services: `docker compose restart`

### Issue: Docker Services Not Starting

**Symptom:** `docker compose up` fails
**Solution:**
1. Check Docker is running: `docker ps`
2. Check ports not in use: `lsof -i :8080,8181,5432,6379,9011,9000`
3. Check logs: `docker compose logs`
4. Clean and restart: `docker compose down -v && docker compose up -d`

---

## Next Steps After Manual Testing

1. **Document Test Results:** Record which scenarios passed/failed
2. **Implement Advanced Tests:** ABAC, ownership, time-based scenarios
3. **Performance Testing:** Load test OPA authorization checks
4. **CI/CD Integration:** Automate testing in pipeline
5. **Production Deployment:** Deploy to staging environment

---

## Additional Resources

- [OPA_TESTING.md](./OPA_TESTING.md) - Detailed OPA architecture and test documentation
- [OPA_TEST_RESULTS.md](./OPA_TEST_RESULTS.md) - Previous test results and analysis
- [README.md](./README.md) - Project overview and features
- [RAILWAY_DEPLOYMENT.md](./RAILWAY_DEPLOYMENT.md) - Production deployment guide

---

**Document Version:** 1.0
**Last Updated:** November 10, 2025
**Author:** Claude Code
**Status:** Ready for Manual Testing
