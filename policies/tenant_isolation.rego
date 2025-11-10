package heimdall.tenant_isolation

import data.heimdall.helpers

# Tenant isolation and multi-tenancy policies

default allow = false

# Core tenant isolation rule: users can only access resources in their tenant
allow if {
    helpers.in_tenant
    helpers.same_tenant
}

# Super admins can cross tenant boundaries
allow if {
    helpers.is_super_admin
}

# Explicit deny for cross-tenant access
deny if {
    not helpers.same_tenant
    not helpers.is_super_admin
    input.resource.tenantId != null
}

# Deny if user has no tenant
deny if {
    input.user.tenantId == ""
    not helpers.is_super_admin
}

# Deny if resource belongs to different tenant
deny if {
    input.user.tenantId != ""
    input.resource.tenantId != ""
    input.user.tenantId != input.resource.tenantId
    not helpers.is_super_admin
}

# Tenant admin permissions

# Tenant admins have full access within their tenant
allow if {
    helpers.has_role("tenant_admin")
    helpers.in_tenant
    helpers.same_tenant
}

# Tenant admins can manage tenant settings
allow if {
    helpers.has_role("tenant_admin")
    input.resource.type == "tenants"
    input.resource.id == input.user.tenantId
}

# Cross-tenant sharing (controlled)

# Allow access to resources explicitly shared across tenants
allow if {
    shared_tenants := input.resource.attributes.shared_with_tenants
    shared_tenants != null
    tenant_share := shared_tenants[_]
    tenant_share.tenant_id == input.user.tenantId
    input.action == tenant_share.permission
}

# Partner tenant access (B2B scenarios)

# Allow access from partner tenants
allow if {
    partner_tenants := input.tenant.settings.partner_tenants
    partner_tenants != null
    user_tenant := input.user.tenantId
    partner := partner_tenants[_]
    partner.tenant_id == user_tenant
    partner.status == "active"
    input.action == partner.allowed_actions[_]
}

# Tenant hierarchy (parent-child relationships)

# Parent tenant users can access child tenant resources
allow if {
    parent_tenant := input.tenant.settings.parent_tenant_id
    parent_tenant != null
    input.user.tenantId == parent_tenant
    helpers.has_role("parent_tenant_admin")
}

# Child tenant admins cannot access parent tenant
deny if {
    child_tenants := input.tenant.settings.child_tenant_ids
    child_tenants != null
    user_tenant := input.user.tenantId
    user_tenant == child_tenants[_]
    input.resource.tenantId == input.tenant.settings.parent_tenant_id
    not helpers.is_super_admin
}

# Multi-tenant resource access

# Global resources (not tenant-specific) can be accessed by all
allow if {
    input.resource.attributes.scope == "global"
    input.action == "read"
}

# Tenant-scoped resources must match user's tenant
deny if {
    input.resource.attributes.scope == "tenant"
    not helpers.same_tenant
    not helpers.is_super_admin
}

# User-scoped resources bypass tenant check if user is owner
allow if {
    input.resource.attributes.scope == "user"
    helpers.is_owner
}

# Tenant quota and limits

# Deny if tenant has exceeded quotas
deny if {
    exceeds_tenant_quota
    not helpers.is_super_admin
}

exceeds_tenant_quota if {
    input.resource.type == "users"
    input.action == "create"
    current_users := input.tenant.settings.current_user_count
    max_users := input.tenant.settings.max_users
    max_users != null
    current_users >= max_users
}

exceeds_tenant_quota if {
    input.resource.type == "roles"
    input.action == "create"
    current_roles := input.tenant.settings.current_role_count
    max_roles := input.tenant.settings.max_roles
    max_roles != null
    current_roles >= max_roles
}

# Tenant status checks

# Deny access if tenant is suspended
deny if {
    input.tenant.settings.status == "suspended"
    not helpers.is_super_admin
}

# Deny access if tenant is inactive
deny if {
    input.tenant.settings.status == "inactive"
    not helpers.is_super_admin
}

# Allow read-only access if tenant is in trial
allow if {
    input.tenant.settings.status == "trial"
    input.action == "read"
    helpers.in_tenant
}

# Deny write access for expired trial tenants
deny if {
    input.tenant.settings.status == "trial"
    trial_expiry := input.tenant.settings.trial_expiry
    current_time := input.time.timestamp
    current_time > trial_expiry
    helpers.is_write_operation
    not helpers.is_super_admin
}

# Tenant data residency

# Ensure data stays within specified regions
deny if {
    data_residency := input.tenant.settings.data_residency
    data_residency != null
    resource_region := input.resource.attributes.region
    resource_region != null
    resource_region != data_residency
    helpers.is_write_operation
}

# Tenant isolation for audit logs

# Users can only read audit logs from their own tenant
allow if {
    input.resource.type == "audit"
    input.action == "read"
    helpers.has_permission("audit.read")
    helpers.in_tenant
    input.resource.tenantId == input.user.tenantId
}

# Cross-tenant operations (explicit allowlist)

# Platform administrators can perform cross-tenant operations
allow if {
    helpers.has_role("platform_admin")
    input.resource.type == "tenants"
}

# Support staff can read across tenants (but not modify)
allow if {
    helpers.has_role("support")
    input.action == "read"
    helpers.has_permission("support.cross_tenant_read")
}

# Billing admins can access billing across tenants
allow if {
    helpers.has_role("billing_admin")
    input.resource.type == "billing"
    helpers.has_permission("billing.manage")
}

# Tenant branding and customization

# Only tenant admins can modify tenant branding
allow if {
    input.resource.type == "tenant_branding"
    helpers.has_role("tenant_admin")
    input.resource.tenantId == input.user.tenantId
}

# Tenant-specific policies

# Respect tenant-specific policy overrides
allow if {
    tenant_policy := input.tenant.settings.custom_policies[_]
    tenant_policy.resource == input.resource.type
    tenant_policy.action == input.action
    tenant_policy.allowed_roles[_] == input.user.roles[_]
    helpers.in_tenant
}

# Managed service provider access

# MSP users can access managed tenants
allow if {
    msp_access := input.user.metadata.msp_access
    msp_access != null
    managed_tenant := msp_access.managed_tenants[_]
    managed_tenant.tenant_id == input.resource.tenantId
    managed_tenant.status == "active"
    input.action == managed_tenant.permissions[_]
}

# Deny MSP access to non-managed tenants
deny if {
    helpers.has_role("msp_user")
    not is_managed_tenant
    not helpers.in_tenant
}

is_managed_tenant if {
    msp_access := input.user.metadata.msp_access
    msp_access != null
    managed_tenant := msp_access.managed_tenants[_]
    managed_tenant.tenant_id == input.resource.tenantId
    managed_tenant.status == "active"
}

# Final decision
default decision = false

decision if {
    allow
    not deny
}
