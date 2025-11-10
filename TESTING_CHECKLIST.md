# Heimdall OPA Integration - Testing Checklist

**Quick reference for testing the OPA integration after Rego syntax fix**

---

## 🚀 Quick Start (15 minutes)

### 1. Start Services
```bash
docker compose up -d
docker compose ps  # Verify all services running
```

### 2. Load Policies
```bash
chmod +x load-policies.sh
./load-policies.sh
```

### 3. Verify OPA
```bash
# Check OPA health
curl http://localhost:8181/health

# Verify policies loaded (should show 7 policies)
curl http://localhost:8181/v1/policies | jq '.result | length'

# Test a policy decision
curl -X POST http://localhost:8181/v1/data/heimdall/authz \
  -H "Content-Type: application/json" \
  -d '{"input":{"user":{"id":"test","roles":["user"],"permissions":[],"tenantId":"t1"},"resource":{"type":"users","id":"test","tenantId":"t1"},"action":"read","tenant":{"id":"t1","status":"active"},"time":{"isBusinessHours":true,"hour":14},"context":{"mfaVerified":false}}}' \
  | jq '.result.allow'  # Should be true (self-access)
```

### 4. Run Integration Tests
```bash
go test -v ./test/integration -run TestOPA -timeout 5m
```

**Expected:** All 23 tests pass ✅

---

## ✅ Test Checklist

### Core Services
- [ ] Heimdall API running (http://localhost:8080/health)
- [ ] OPA running (http://localhost:8181/health)
- [ ] PostgreSQL running
- [ ] Redis running (redis-cli ping)
- [ ] FusionAuth running (http://localhost:9011/api/status)
- [ ] MinIO running (http://localhost:9001)

### OPA Policies
- [ ] All 7 policies loaded without errors
- [ ] authz.rego loads
- [ ] rbac.rego loads
- [ ] abac.rego loads
- [ ] resource_ownership.rego loads
- [ ] time_based.rego loads
- [ ] tenant_isolation.rego loads
- [ ] helpers.rego loads

### Integration Tests (23 total)
#### TestOPARBACBasicPermissions (3 tests)
- [ ] Regular user cannot list all users (403)
- [ ] User can access own profile (200)
- [ ] User can update own profile (200)

#### TestOPATenantIsolation (2 tests)
- [ ] User can only access resources in their tenant
- [ ] User permissions retrieved correctly

#### TestOPAProtectedEndpoints (3 tests)
- [ ] Policy endpoints require permissions (403 without)
- [ ] Bundle endpoints require permissions (403 without)
- [ ] Tenant endpoints require permissions (403 without)

#### TestOPAAuthenticationRequired (3 tests)
- [ ] Protected endpoints reject unauthenticated (401)
- [ ] Invalid token rejected (401)
- [ ] Expired token rejected (401)

#### TestOPASelfAccessRules (4 tests)
- [ ] User can read own data (200)
- [ ] User can update own data (200)
- [ ] User can delete own account (200)
- [ ] User can retrieve own permissions (200)

#### TestOPAUserManagementPermissions (3 tests)
- [ ] Regular user cannot list all users (403)
- [ ] Regular user cannot view other users (403)
- [ ] Regular user cannot assign roles (403)

#### TestOPATokenValidation (3 tests)
- [ ] Valid token allows access (200)
- [ ] Missing token denies access (401)
- [ ] Malformed token denies access (401)

#### TestOPASessionManagement (2 tests)
- [ ] Logout invalidates session
- [ ] Refresh token extends session

### Manual API Tests
#### Authentication
- [ ] Register user successful
- [ ] Login successful
- [ ] Token received

#### Self-Access (Regular User)
- [ ] GET /v1/users/:id (own ID) → 200 OK
- [ ] PUT /v1/users/:id (own ID) → 200 OK
- [ ] GET /v1/users/:id (other ID) → 403 Forbidden

#### Admin Endpoints (Regular User)
- [ ] GET /v1/users → 403 Forbidden
- [ ] GET /v1/policies → 403 Forbidden
- [ ] GET /v1/bundles → 403 Forbidden

#### Admin Access (Admin User)
- [ ] GET /v1/users → 200 OK
- [ ] GET /v1/policies → 200 OK
- [ ] GET /v1/bundles → 200 OK
- [ ] POST /v1/policies → 201 Created
- [ ] POST /v1/bundles → 201 Created

---

## 🎯 Success Criteria

### Minimum (MVP)
- ✅ All services running
- ✅ All policies load without errors
- ✅ 23/23 integration tests pass
- ✅ Self-access works (users can access own resources)
- ✅ Admin protection works (regular users blocked from admin endpoints)

### Complete
- ✅ All manual tests pass
- ✅ Policy management works (create, publish, rollback)
- ✅ Bundle management works (create, activate, deploy)
- ✅ Performance acceptable (<100ms avg response time)
- ✅ No errors in logs

---

## 🐛 Common Issues & Solutions

### Issue: Tests fail with "connection refused"
**Solution:** Start services: `docker compose up -d`

### Issue: Tests fail with 500 errors
**Solution:** Check OPA logs: `docker compose logs opa`
**Verify policies loaded:** `./load-policies.sh`

### Issue: Tests fail with 403 when expecting 200
**Solution:** Check user has correct roles/permissions
**Check OPA decision:** Test policy manually with curl

### Issue: OPA policies have parse errors
**Solution:** This should be FIXED now! If you still see parse errors:
1. Verify you're on latest feat/OPA branch
2. Check policies have `if` keyword: `grep -n "allow {" policies/*.rego`
3. Should be ZERO matches (all should have `allow if {`)

---

## 📊 Expected Results Summary

| Test Category | Tests | Expected Pass | Status |
|---------------|-------|---------------|--------|
| RBAC Basic | 3 | 3 | ⏳ |
| Tenant Isolation | 2 | 2 | ⏳ |
| Protected Endpoints | 3 | 3 | ⏳ |
| Authentication | 3 | 3 | ⏳ |
| Self-Access | 4 | 4 | ⏳ |
| User Management | 3 | 3 | ⏳ |
| Token Validation | 3 | 3 | ⏳ |
| Session Management | 2 | 2 | ⏳ |
| **TOTAL** | **23** | **23** | **⏳** |

**Previous Status (Before Fix):** 15/23 passing (8 blocked by Rego syntax)
**Current Status (After Fix):** Ready to test - expect 23/23 ✅

---

## 📝 Test Result Template

After running tests, fill this out:

```
Date: __________
Tester: __________
Branch: feat/OPA
Commit: __________

Services Running: [ ] Yes [ ] No
Policies Loaded: [ ] Yes [ ] No
Integration Tests: __/23 passed
Manual Tests: __/15 passed

Issues Found:
1.
2.
3.

Notes:


Overall Status: [ ] Pass [ ] Fail
```

---

## 🔗 Related Documentation

- [MANUAL_TESTING_GUIDE.md](./MANUAL_TESTING_GUIDE.md) - Detailed step-by-step testing
- [OPA_IMPLEMENTATION_STATUS.md](./OPA_IMPLEMENTATION_STATUS.md) - Complete status report
- [OPA_TESTING.md](./OPA_TESTING.md) - Architecture and design documentation
- [OPA_TEST_RESULTS.md](./OPA_TEST_RESULTS.md) - Previous test results (before fix)

---

**Document Version:** 1.0
**Created:** November 10, 2025
**Status:** Ready for Testing
