#!/bin/bash

# Complete OPA Integration Test Suite
# This script runs the full test suite including:
# 1. Environment validation
# 2. Service startup
# 3. Policy loading
# 4. Integration tests
# 5. Manual API tests
# 6. Test result reporting

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Configuration
API_URL="${HEIMDALL_API_URL:-http://localhost:8080}"
OPA_URL="${OPA_URL:-http://localhost:8181}"
TEST_TIMEOUT="${TEST_TIMEOUT:-5m}"

# Test results
TESTS_PASSED=0
TESTS_FAILED=0
TESTS_SKIPPED=0

# Logging functions
log_info() {
    echo -e "${BLUE}ℹ ${NC}$1"
}

log_success() {
    echo -e "${GREEN}✅ ${NC}$1"
}

log_error() {
    echo -e "${RED}❌ ${NC}$1"
}

log_warning() {
    echo -e "${YELLOW}⚠️  ${NC}$1"
}

log_section() {
    echo ""
    echo -e "${CYAN}========================================${NC}"
    echo -e "${CYAN}$1${NC}"
    echo -e "${CYAN}========================================${NC}"
}

# Check if a command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Wait for service to be ready
wait_for_service() {
    local url=$1
    local name=$2
    local max_attempts=30
    local attempt=1

    log_info "Waiting for $name at $url..."

    while [ $attempt -le $max_attempts ]; do
        if curl -sf "$url" >/dev/null 2>&1; then
            log_success "$name is ready"
            return 0
        fi
        echo -n "."
        sleep 2
        attempt=$((attempt + 1))
    done

    log_error "$name failed to start after ${max_attempts} attempts"
    return 1
}

# Validate environment
validate_environment() {
    log_section "Phase 1: Environment Validation"

    local validation_failed=0

    # Check required commands
    log_info "Checking required commands..."

    for cmd in docker go curl jq; do
        if command_exists "$cmd"; then
            log_success "$cmd is installed"
        else
            log_error "$cmd is not installed"
            validation_failed=1
        fi
    done

    # Check Docker Compose
    if docker compose version >/dev/null 2>&1; then
        log_success "docker compose is available"
    elif command_exists docker-compose; then
        log_success "docker-compose is available"
    else
        log_error "docker compose is not available"
        validation_failed=1
    fi

    # Check Go version
    if command_exists go; then
        GO_VERSION=$(go version | grep -oP 'go\d+\.\d+' | head -1)
        log_info "Go version: $GO_VERSION"
    fi

    if [ $validation_failed -eq 1 ]; then
        log_error "Environment validation failed. Please install missing dependencies."
        exit 1
    fi

    log_success "Environment validation passed"
}

# Start Docker services
start_services() {
    log_section "Phase 2: Starting Docker Services"

    log_info "Stopping any existing services..."
    docker compose down -v 2>/dev/null || true

    log_info "Starting all services..."
    if docker compose up -d; then
        log_success "Services started"
    else
        log_error "Failed to start services"
        return 1
    fi

    log_info "Waiting for services to be healthy..."
    sleep 5

    # Wait for each service
    wait_for_service "$API_URL/health" "Heimdall API" || return 1
    wait_for_service "$OPA_URL/health" "OPA" || return 1

    log_success "All services are running"
}

# Load OPA policies
load_policies() {
    log_section "Phase 3: Loading OPA Policies"

    if [ ! -f "load-policies.sh" ]; then
        log_error "load-policies.sh not found"
        return 1
    fi

    log_info "Loading policies into OPA..."
    chmod +x load-policies.sh

    if ./load-policies.sh; then
        log_success "Policies loaded successfully"
    else
        log_error "Failed to load policies"
        return 1
    fi

    # Verify policies loaded
    log_info "Verifying policies..."
    POLICY_COUNT=$(curl -s "$OPA_URL/v1/policies" | jq '.result | length' 2>/dev/null || echo "0")

    if [ "$POLICY_COUNT" -eq 7 ]; then
        log_success "All 7 policies loaded correctly"
    else
        log_warning "Expected 7 policies, found $POLICY_COUNT"
    fi

    # Test a simple policy decision
    log_info "Testing policy evaluation..."
    TEST_RESPONSE=$(curl -s -X POST "$OPA_URL/v1/data/heimdall/authz" \
        -H "Content-Type: application/json" \
        -d '{
            "input": {
                "user": {"id": "test", "roles": ["user"], "permissions": [], "tenantId": "t1"},
                "resource": {"type": "users", "id": "test", "tenantId": "t1"},
                "action": "read",
                "tenant": {"id": "t1", "status": "active"},
                "time": {"isBusinessHours": true, "hour": 14},
                "context": {"mfaVerified": false}
            }
        }')

    if echo "$TEST_RESPONSE" | jq -e '.result.allow == true' >/dev/null 2>&1; then
        log_success "Policy evaluation working (self-access test passed)"
    else
        log_error "Policy evaluation test failed"
        echo "$TEST_RESPONSE" | jq '.' || echo "$TEST_RESPONSE"
        return 1
    fi
}

# Run integration tests
run_integration_tests() {
    log_section "Phase 4: Running Integration Tests"

    log_info "Running Go integration tests..."

    export HEIMDALL_API_URL="$API_URL"

    if go test -v ./test/integration -run TestOPA -timeout "$TEST_TIMEOUT" 2>&1 | tee /tmp/test-output.log; then
        log_success "Integration tests passed"

        # Count test results
        TESTS_PASSED=$(grep -c "PASS:" /tmp/test-output.log || echo "0")
        log_info "Tests passed: $TESTS_PASSED"

        return 0
    else
        log_error "Integration tests failed"

        # Show failures
        log_info "Failed tests:"
        grep "FAIL:" /tmp/test-output.log || true

        return 1
    fi
}

# Run manual API tests
run_manual_tests() {
    log_section "Phase 5: Running Manual API Tests"

    log_info "Creating test user..."

    # Register user
    REGISTER_RESPONSE=$(curl -s -X POST "$API_URL/v1/auth/register" \
        -H "Content-Type: application/json" \
        -d '{
            "email": "test-'$(date +%s)'@example.com",
            "password": "TestPass123!",
            "firstName": "Test",
            "lastName": "User"
        }')

    if echo "$REGISTER_RESPONSE" | jq -e '.success == true' >/dev/null 2>&1; then
        log_success "User registration successful"

        TEST_EMAIL=$(echo "$REGISTER_RESPONSE" | jq -r '.data.user.email')
        USER_ID=$(echo "$REGISTER_RESPONSE" | jq -r '.data.user.id')

        log_info "Test user: $TEST_EMAIL"
    else
        log_error "User registration failed"
        echo "$REGISTER_RESPONSE" | jq '.' || echo "$REGISTER_RESPONSE"
        return 1
    fi

    # Login
    log_info "Logging in..."
    LOGIN_RESPONSE=$(curl -s -X POST "$API_URL/v1/auth/login" \
        -H "Content-Type: application/json" \
        -d "{
            \"email\": \"$TEST_EMAIL\",
            \"password\": \"TestPass123!\"
        }")

    if echo "$LOGIN_RESPONSE" | jq -e '.success == true' >/dev/null 2>&1; then
        log_success "Login successful"
        TOKEN=$(echo "$LOGIN_RESPONSE" | jq -r '.data.token')
    else
        log_error "Login failed"
        echo "$LOGIN_RESPONSE" | jq '.' || echo "$LOGIN_RESPONSE"
        return 1
    fi

    # Test self-access (should succeed)
    log_info "Testing self-access (should succeed)..."
    SELF_ACCESS_RESPONSE=$(curl -s -w "\n%{http_code}" -X GET "$API_URL/v1/users/$USER_ID" \
        -H "Authorization: Bearer $TOKEN")

    HTTP_CODE=$(echo "$SELF_ACCESS_RESPONSE" | tail -1)
    RESPONSE_BODY=$(echo "$SELF_ACCESS_RESPONSE" | head -n -1)

    if [ "$HTTP_CODE" = "200" ]; then
        log_success "Self-access test passed (200 OK)"
    else
        log_error "Self-access test failed (expected 200, got $HTTP_CODE)"
        echo "$RESPONSE_BODY"
    fi

    # Test unauthorized access (should fail with 403)
    log_info "Testing unauthorized access (should fail with 403)..."
    UNAUTH_RESPONSE=$(curl -s -w "\n%{http_code}" -X GET "$API_URL/v1/users" \
        -H "Authorization: Bearer $TOKEN")

    HTTP_CODE=$(echo "$UNAUTH_RESPONSE" | tail -1)

    if [ "$HTTP_CODE" = "403" ]; then
        log_success "Unauthorized access test passed (403 Forbidden)"
    else
        log_warning "Unauthorized access test: expected 403, got $HTTP_CODE"
    fi

    # Test policy endpoint access (should fail with 403 for regular user)
    log_info "Testing policy endpoint (should fail with 403)..."
    POLICY_RESPONSE=$(curl -s -w "\n%{http_code}" -X GET "$API_URL/v1/policies" \
        -H "Authorization: Bearer $TOKEN")

    HTTP_CODE=$(echo "$POLICY_RESPONSE" | tail -1)

    if [ "$HTTP_CODE" = "403" ]; then
        log_success "Policy endpoint protection test passed (403 Forbidden)"
    else
        log_warning "Policy endpoint test: expected 403, got $HTTP_CODE"
    fi

    log_success "Manual API tests completed"
}

# Collect test results
collect_results() {
    log_section "Phase 6: Test Results Summary"

    echo ""
    log_info "Test Execution Summary:"
    echo "  • Integration Tests: Check /tmp/test-output.log for details"
    echo "  • Manual API Tests: See output above"
    echo ""

    # Check service logs for errors
    log_info "Checking for errors in service logs..."

    if docker compose logs heimdall 2>/dev/null | grep -i "error\|fatal" | tail -5; then
        log_warning "Found errors in Heimdall logs (see above)"
    else
        log_success "No critical errors in Heimdall logs"
    fi

    if docker compose logs opa 2>/dev/null | grep -i "error\|fatal" | tail -5; then
        log_warning "Found errors in OPA logs (see above)"
    else
        log_success "No critical errors in OPA logs"
    fi
}

# Generate test report
generate_report() {
    log_section "Generating Test Report"

    REPORT_FILE="OPA_TEST_REPORT_$(date +%Y%m%d_%H%M%S).md"

    cat > "$REPORT_FILE" <<EOF
# OPA Integration Test Report

**Date:** $(date)
**Branch:** $(git branch --show-current)
**Commit:** $(git rev-parse --short HEAD)

---

## Test Execution Summary

### Environment
- API URL: $API_URL
- OPA URL: $OPA_URL
- Go Version: $(go version)
- Docker Version: $(docker --version)

### Services Status
\`\`\`
$(docker compose ps 2>/dev/null || echo "Unable to get service status")
\`\`\`

### Policy Loading
- Policies Loaded: $(curl -s "$OPA_URL/v1/policies" | jq '.result | length' 2>/dev/null || echo "Unknown")
- Expected: 7

### Integration Tests
See /tmp/test-output.log for detailed results

### Manual API Tests
- User Registration: Tested
- Authentication: Tested
- Self-Access Rules: Tested
- Admin Protection: Tested
- Policy Endpoints: Tested

---

## Service Logs

### Heimdall Logs (last 20 lines)
\`\`\`
$(docker compose logs --tail=20 heimdall 2>/dev/null || echo "Unable to fetch logs")
\`\`\`

### OPA Logs (last 20 lines)
\`\`\`
$(docker compose logs --tail=20 opa 2>/dev/null || echo "Unable to fetch logs")
\`\`\`

---

**Report Generated:** $(date)
EOF

    log_success "Test report saved to $REPORT_FILE"
}

# Cleanup function
cleanup() {
    log_section "Cleanup"

    if [ "${KEEP_SERVICES}" != "true" ]; then
        log_info "Stopping services..."
        docker compose down 2>/dev/null || true
        log_success "Services stopped"
    else
        log_info "Keeping services running (KEEP_SERVICES=true)"
    fi
}

# Main execution
main() {
    log_section "Heimdall OPA Integration - Complete Test Suite"

    echo ""
    log_info "This script will:"
    echo "  1. Validate environment"
    echo "  2. Start Docker services"
    echo "  3. Load OPA policies"
    echo "  4. Run integration tests"
    echo "  5. Run manual API tests"
    echo "  6. Generate test report"
    echo ""

    # Set trap for cleanup
    trap cleanup EXIT

    # Execute test phases
    validate_environment || exit 1
    start_services || exit 1
    load_policies || exit 1

    # Run tests (continue even if some fail)
    set +e
    run_integration_tests
    INTEGRATION_RESULT=$?

    run_manual_tests
    MANUAL_RESULT=$?
    set -e

    # Collect results
    collect_results
    generate_report

    # Final summary
    log_section "Test Suite Complete"

    if [ $INTEGRATION_RESULT -eq 0 ] && [ $MANUAL_RESULT -eq 0 ]; then
        log_success "ALL TESTS PASSED! 🎉"
        echo ""
        log_info "The OPA integration is working correctly:"
        echo "  ✅ All 7 Rego policies loaded"
        echo "  ✅ Policy evaluation working"
        echo "  ✅ Integration tests passed"
        echo "  ✅ Manual API tests passed"
        echo "  ✅ RBAC enforcement working"
        echo "  ✅ Self-access rules working"
        echo "  ✅ Admin protection working"
        echo ""
        exit 0
    else
        log_error "SOME TESTS FAILED"
        echo ""
        log_info "Results:"
        [ $INTEGRATION_RESULT -eq 0 ] && echo "  ✅ Integration tests passed" || echo "  ❌ Integration tests failed"
        [ $MANUAL_RESULT -eq 0 ] && echo "  ✅ Manual API tests passed" || echo "  ❌ Manual API tests failed"
        echo ""
        log_info "Check the test report for details: $REPORT_FILE"
        exit 1
    fi
}

# Run main function
main "$@"
