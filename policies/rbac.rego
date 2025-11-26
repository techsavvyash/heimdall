package heimdall.rbac

import data.heimdall.helpers

# Role-Based Access Control (RBAC) rules

# Default deny
default allow = false

# Super admins can do anything
allow if {
    helpers.is_super_admin
}

# Admins have broad permissions within their tenant
allow if {
    helpers.is_admin
    helpers.in_tenant
}

# Role-based permission checks
allow if {
    # User must be in the correct tenant
    helpers.in_tenant

    # Check if user has the required permission
    permission := sprintf("%s.%s", [input.resource.type, input.action])
    helpers.has_permission(permission)
}

# Allow scoped permissions (e.g., users.read.own)
allow if {
    helpers.in_tenant
    scope := "own"
    permission := sprintf("%s.%s.%s", [input.resource.type, input.action, scope])
    helpers.has_permission(permission)
    helpers.is_owner
}

# Tenant-scoped permissions
allow if {
    helpers.in_tenant
    scope := "tenant"
    permission := sprintf("%s.%s.%s", [input.resource.type, input.action, scope])
    helpers.has_permission(permission)
    helpers.same_tenant
}

# Global-scoped permissions (cross-tenant)
allow if {
    scope := "global"
    permission := sprintf("%s.%s.%s", [input.resource.type, input.action, scope])
    helpers.has_permission(permission)
}

# Role hierarchy rules
# Admins inherit all non-admin role permissions
allow if {
    helpers.is_admin
    helpers.in_tenant
    not is_admin_only_permission
}

is_admin_only_permission if {
    startswith(input.resource.type, "system.")
}

is_admin_only_permission if {
    input.resource.type == "roles"
    input.action == "delete"
}

is_admin_only_permission if {
    input.resource.type == "permissions"
}

# Specific resource type permissions

# Users can read their own profile
allow if {
    input.resource.type == "users"
    input.action == "read"
    helpers.is_self_access
}

# Users can update their own profile
allow if {
    input.resource.type == "users"
    input.action == "update"
    helpers.is_self_access
    helpers.in_tenant
}

# Users can read roles within their tenant
allow if {
    input.resource.type == "roles"
    input.action == "read"
    helpers.in_tenant
}

# Only admins can manage roles
allow if {
    input.resource.type == "roles"
    helpers.is_write_operation
    helpers.is_admin
    helpers.in_tenant
}

# Only admins can assign roles
allow if {
    input.resource.type == "roles"
    input.action == "assign"
    helpers.is_admin
    helpers.in_tenant
}

# Audit logs are read-only and require special permission
allow if {
    input.resource.type == "audit"
    input.action == "read"
    helpers.has_permission("audit.read")
    helpers.in_tenant
}

# Tenant management
# Users can read their own tenant only
allow if {
    input.resource.type == "tenants"
    input.action == "read"
    input.resource.id == input.user.tenantId
    helpers.in_tenant
}

# Admins can read any tenant in their context
allow if {
    input.resource.type == "tenants"
    input.action == "read"
    helpers.is_admin
    helpers.in_tenant
}

allow if {
    input.resource.type == "tenants"
    helpers.is_write_operation
    helpers.is_admin
    helpers.in_tenant
}

# Policy management - only admins
allow if {
    input.resource.type == "policies"
    helpers.is_admin
    helpers.in_tenant
}

# Bundle management - only admins
allow if {
    input.resource.type == "bundles"
    helpers.is_admin
    helpers.in_tenant
}

# Deny rules (explicit denials take precedence)
deny if {
    # Cannot delete system permissions
    input.resource.type == "permissions"
    input.action == "delete"
    input.resource.attributes.isSystem == true
}

deny if {
    # Cannot delete system roles
    input.resource.type == "roles"
    input.action == "delete"
    input.resource.attributes.isSystem == true
}

deny if {
    # Cannot modify super admin role
    input.resource.type == "roles"
    helpers.is_write_operation
    input.resource.attributes.name == "super_admin"
    not helpers.is_super_admin
}

# Final decision (deny takes precedence over allow)
default decision = false

decision if {
    allow
    not deny
}
