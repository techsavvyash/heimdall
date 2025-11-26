# Heimdall Authorization Documentation

Heimdall uses Open Policy Agent (OPA) for fine-grained authorization with support for RBAC, ABAC, tenant isolation, and resource ownership.

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [OPA Integration](#opa-integration)
3. [Policy Structure](#policy-structure)
4. [RBAC Implementation](#rbac-implementation)
5. [Tenant Isolation](#tenant-isolation)
6. [Permission Scopes](#permission-scopes)
7. [Protected Endpoints](#protected-endpoints)
8. [Writing Custom Policies](#writing-custom-policies)
9. [Policy Management API](#policy-management-api)
10. [Troubleshooting](#troubleshooting)

---

## Architecture Overview

```
Request with JWT
       |
       v
+------------------+
|  Auth Middleware |  (JWT validation)
+------------------+
       |
       v
+------------------+
|  OPA Middleware  |  (Build context, query OPA)
+------------------+
       |
       v
+------------------+
|   OPA Server     |  (Evaluate policies)
+------------------+
       |
       v
+------------------+
|  Policy Decision |  (allow/deny + metadata)
+------------------+
```

### Components

- **OPA Client**: HTTP client for OPA REST API
- **OPA Evaluator**: High-level evaluation with caching
- **Context Builder**: Constructs authorization input from request
- **Policy Files**: Rego policies for authorization rules

---

## OPA Integration

### Configuration

```bash
# Environment variables
OPA_URL=http://localhost:8181
OPA_POLICY_PATH=heimdall/authz
OPA_TIMEOUT_SECONDS=5
OPA_ENABLE_CACHE=true
```

### Loading Policies

Policies must be loaded into OPA before the API starts. Use the provided script:

```bash
./load-policies.sh
```

This loads all 7 policy files in the correct order:

1. helpers.rego (dependency for all others)
2. authz.rego (main entry point)
3. rbac.rego
4. abac.rego
5. tenant_isolation.rego
6. resource_ownership.rego
7. time_based.rego

### Verifying Policies

```bash
# Check OPA health
curl http://localhost:8181/health

# List loaded policies
curl http://localhost:8181/v1/policies

# Test a policy query
curl -X POST http://localhost:8181/v1/data/heimdall/authz/allow \
  -H "Content-Type: application/json" \
  -d '{"input": {"user": {"roles": ["admin"]}, "action": "read", "resource": {"type": "users"}}}'
```

---

## Policy Structure

### Authorization Input

Every OPA query includes this context structure:

```json
{
  "input": {
    "user": {
      "id": "user-uuid",
      "email": "user@example.com",
      "roles": ["user", "admin"],
      "permissions": ["users:read", "users:update"],
      "tenantId": "tenant-uuid",
      "metadata": {}
    },
    "resource": {
      "type": "users",
      "id": "resource-uuid",
      "ownerId": "owner-uuid",
      "tenantId": "tenant-uuid",
      "attributes": {}
    },
    "action": "read",
    "time": {
      "timestamp": 1699999999,
      "dayOfWeek": "Monday",
      "hour": 14,
      "minute": 30,
      "isWeekend": false,
      "isBusinessHours": true
    },
    "context": {
      "ipAddress": "192.168.1.1",
      "userAgent": "Mozilla/5.0...",
      "method": "GET",
      "path": "/v1/users/123",
      "mfaVerified": false,
      "sessionAge": 3600
    },
    "tenant": {
      "id": "tenant-uuid",
      "slug": "acme",
      "settings": {}
    }
  }
}
```

### Policy Output

```json
{
  "allow": true,
  "reasons": ["user has users:read permission"],
  "errors": []
}
```

---

## RBAC Implementation

### Database Schema

```sql
-- Roles table
CREATE TABLE roles (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    name VARCHAR(100) NOT NULL,
    description TEXT,
    parent_role_id UUID,
    is_system BOOLEAN DEFAULT false
);

-- Permissions table
CREATE TABLE permissions (
    id UUID PRIMARY KEY,
    name VARCHAR(100) UNIQUE NOT NULL,
    resource VARCHAR(100) NOT NULL,
    action VARCHAR(50) NOT NULL,
    scope VARCHAR(50) DEFAULT 'tenant'
);

-- Role-Permission mapping
CREATE TABLE role_permissions (
    role_id UUID REFERENCES roles(id),
    permission_id UUID REFERENCES permissions(id),
    PRIMARY KEY (role_id, permission_id)
);

-- User-Role mapping
CREATE TABLE user_roles (
    user_id UUID REFERENCES users(id),
    role_id UUID REFERENCES roles(id),
    assigned_by UUID,
    assigned_at TIMESTAMP DEFAULT NOW(),
    expires_at TIMESTAMP,
    PRIMARY KEY (user_id, role_id)
);
```

### Permission Format

Permissions follow the pattern: `resource:action:scope`

Examples:
- `users:read` - Read users (tenant scope)
- `users:read:own` - Read own user data
- `users:read:global` - Read all users globally
- `policies:create` - Create policies
- `bundles:deploy` - Deploy bundles

### Role Hierarchy

Roles can inherit from parent roles:

```
super_admin
    |
    +-- admin
        |
        +-- manager
            |
            +-- user
```

Child roles inherit all permissions from parent roles.

### Assigning Roles

```http
POST /v1/users/{userId}/roles
Authorization: Bearer <admin_token>
Content-Type: application/json

{
  "roleId": "role-uuid"
}
```

### Removing Roles

```http
DELETE /v1/users/{userId}/roles/{roleId}
Authorization: Bearer <admin_token>
```

---

## Tenant Isolation

### Multi-Tenant Architecture

Every resource belongs to exactly one tenant. Users can only access resources within their tenant.

### Tenant Context

The tenant is extracted from:

1. JWT claims (`tenantId`)
2. X-Tenant-ID header (for cross-tenant access if permitted)
3. Subdomain (e.g., `acme.heimdall.example.com`)

### Isolation Rules

```rego
# From tenant_isolation.rego
allow if {
    input.user.tenantId == input.resource.tenantId
}

# Deny cross-tenant access
deny if {
    input.user.tenantId != input.resource.tenantId
    not is_super_admin
}
```

### Cross-Tenant Access

Super admins can access resources across tenants. MSP (Managed Service Provider) users may have limited cross-tenant access for specific operations.

---

## Permission Scopes

### Scope Types

| Scope | Description |
|-------|-------------|
| `own` | User's own resources only |
| `tenant` | All resources in user's tenant |
| `global` | All resources system-wide |

### Scope Evaluation

```rego
# Check permission with scope
has_scoped_permission(resource, action, scope) if {
    perm := sprintf("%s:%s:%s", [resource, action, scope])
    perm == input.user.permissions[_]
}

# User can read own profile
allow if {
    input.resource.type == "users"
    input.action == "read"
    input.user.id == input.resource.id
}

# User with users:read:tenant can read any user in tenant
allow if {
    input.resource.type == "users"
    input.action == "read"
    has_scoped_permission("users", "read", "tenant")
}
```

---

## Protected Endpoints

### User Management

| Endpoint | Required Permission |
|----------|-------------------|
| GET /v1/users | users:read |
| GET /v1/users/:id | users:read |
| POST /v1/users/:id/roles | roles:assign |
| DELETE /v1/users/:id/roles/:roleId | roles:assign |

### Tenant Management

| Endpoint | Required Permission |
|----------|-------------------|
| GET /v1/tenants | tenants:read |
| POST /v1/tenants | tenants:create |
| GET /v1/tenants/:id | tenants:read |
| PATCH /v1/tenants/:id | tenants:update |
| DELETE /v1/tenants/:id | tenants:delete |
| POST /v1/tenants/:id/suspend | tenants:suspend |
| POST /v1/tenants/:id/activate | tenants:activate |

### Policy Management

| Endpoint | Required Permission |
|----------|-------------------|
| GET /v1/policies | policies:read |
| POST /v1/policies | policies:create |
| GET /v1/policies/:id | policies:read |
| PUT /v1/policies/:id | policies:update |
| DELETE /v1/policies/:id | policies:delete |
| POST /v1/policies/:id/publish | policies:publish |
| POST /v1/policies/:id/validate | policies:test |
| POST /v1/policies/:id/test | policies:test |

### Bundle Management

| Endpoint | Required Permission | MFA Required |
|----------|-------------------|--------------|
| GET /v1/bundles | bundles:read | No |
| POST /v1/bundles | bundles:create | No |
| GET /v1/bundles/:id | bundles:read | No |
| POST /v1/bundles/:id/activate | bundles:activate | Yes |
| POST /v1/bundles/:id/deploy | bundles:deploy | Yes |
| DELETE /v1/bundles/:id | bundles:delete | No |

---

## Writing Custom Policies

### Policy File Structure

```rego
package heimdall.custom

import data.heimdall.helpers

# Default deny
default allow = false

# Custom rule
allow if {
    input.resource.type == "reports"
    input.action == "read"
    helpers.has_role("analyst")
}
```

### Helper Functions

Available helpers from `helpers.rego`:

```rego
# Role checks
helpers.has_role(role)
helpers.has_any_role(roles)
helpers.has_all_roles(roles)
helpers.is_admin
helpers.is_super_admin

# Permission checks
helpers.has_permission(permission)
helpers.has_any_permission(permissions)
helpers.has_resource_permission(resource, action)
helpers.has_scoped_permission(resource, action, scope)

# Context checks
helpers.is_owner
helpers.same_tenant
helpers.is_mfa_verified
helpers.is_business_hours
helpers.is_session_fresh(max_age_seconds)
helpers.in_time_window(start_hour, end_hour)

# Action checks
helpers.is_action(action)
helpers.is_read_operation
helpers.is_write_operation
helpers.is_resource_type(type)
```

### Testing Policies

Create test cases in the policy:

```rego
test_allow_analyst_read_reports if {
    allow with input as {
        "user": {"roles": ["analyst"]},
        "resource": {"type": "reports"},
        "action": "read"
    }
}

test_deny_user_read_reports if {
    not allow with input as {
        "user": {"roles": ["user"]},
        "resource": {"type": "reports"},
        "action": "read"
    }
}
```

Run tests:

```bash
opa test policies/ -v
```

---

## Policy Management API

### Create Policy

```http
POST /v1/policies
Authorization: Bearer <admin_token>
Content-Type: application/json

{
  "name": "Custom Report Access",
  "description": "Controls access to reports",
  "path": "heimdall/custom/reports",
  "type": "rego",
  "content": "package heimdall.custom.reports\n\ndefault allow = false\n\nallow if {\n    input.user.roles[_] == \"analyst\"\n}"
}
```

### Validate Policy

```http
POST /v1/policies/{id}/validate
Authorization: Bearer <admin_token>
```

Response:

```json
{
  "success": true,
  "data": {
    "valid": true,
    "errors": []
  }
}
```

### Test Policy

```http
POST /v1/policies/{id}/test
Authorization: Bearer <admin_token>
```

Response:

```json
{
  "success": true,
  "data": {
    "results": [
      {"name": "test_allow_analyst", "passed": true},
      {"name": "test_deny_user", "passed": true}
    ],
    "passed": 2,
    "failed": 0
  }
}
```

### Publish Policy

```http
POST /v1/policies/{id}/publish
Authorization: Bearer <admin_token>
```

### Create Bundle

Bundle multiple policies for deployment:

```http
POST /v1/bundles
Authorization: Bearer <admin_token>
Content-Type: application/json

{
  "name": "Production Bundle",
  "description": "All production policies",
  "policyIds": ["policy-uuid-1", "policy-uuid-2"]
}
```

### Deploy Bundle

Requires MFA:

```http
POST /v1/bundles/{id}/deploy
Authorization: Bearer <admin_token>
X-MFA-Verified: true
```

---

## Troubleshooting

### Policy Not Loaded

```bash
# Check OPA logs
docker logs heimdall-opa

# Reload policies
./load-policies.sh

# Verify policy is loaded
curl http://localhost:8181/v1/policies/authz
```

### Authorization Denied

```bash
# Test the policy directly
curl -X POST http://localhost:8181/v1/data/heimdall/authz \
  -H "Content-Type: application/json" \
  -d '{
    "input": {
      "user": {"id": "user-id", "roles": ["user"], "permissions": []},
      "resource": {"type": "users"},
      "action": "read"
    }
  }'
```

### Cache Issues

Clear OPA decision cache:

```bash
# Clear Redis cache for a user
redis-cli DEL "opa:cache:user-uuid"

# Or restart the API to clear all caches
docker compose restart heimdall
```

### Common Errors

| Error | Cause | Solution |
|-------|-------|----------|
| "undefined function" | Helper not loaded | Reload helpers.rego first |
| "rego_parse_error" | Syntax error | Check `if` keyword usage |
| "policy not found" | Policy not loaded | Run load-policies.sh |
| "403 Forbidden" | Missing permission | Check user roles/permissions |

### Debug Mode

Enable verbose logging:

```bash
ENVIRONMENT=development
LOG_LEVEL=debug
```

---

## Performance Considerations

### Caching

OPA decisions are cached in Redis for 5 minutes:

```
Key: opa:cache:{userId}:{resourceType}:{action}:{resourceId}
TTL: 300 seconds
```

### Cache Invalidation

Cache is automatically invalidated when:
- User roles change
- User permissions change
- Policy is updated

Manual invalidation:

```go
evaluator.InvalidateUserCache(ctx, userID)
```

### Batch Permission Checks

For checking multiple permissions:

```go
results := evaluator.BatchCheckPermissions(ctx, user, []Permission{
    {Resource: "users", Action: "read"},
    {Resource: "policies", Action: "create"},
})
```

### Filtering Resources

Filter a list of resources by permission:

```go
allowedResources := evaluator.FilterAllowed(ctx, user, resources, "read")
```

---

## Security Considerations

1. **Default Deny**: All policies start with `default allow = false`
2. **Explicit Grants**: Permissions must be explicitly granted
3. **Audit Logging**: All authorization decisions are logged
4. **MFA for Sensitive Operations**: Bundle deployment requires MFA
5. **Time-Based Access**: Support for business hours restrictions
6. **Tenant Isolation**: Enforced at policy level, not just application level
