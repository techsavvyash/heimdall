package heimdall.abac

import data.heimdall.helpers

# Attribute-Based Access Control (ABAC) rules
# These rules use contextual attributes beyond roles and permissions

default allow = false

# Time-based access control

# Allow access during business hours for specific resources
allow if {
    input.resource.type == "reports"
    input.action == "read"
    helpers.is_business_hours
    helpers.in_tenant
}

# Restrict sensitive operations to business hours
allow if {
    input.resource.type == "users"
    input.action == "delete"
    helpers.is_business_hours
    helpers.is_admin
    helpers.in_tenant
}

# Allow weekend access for on-call staff
allow if {
    helpers.is_weekend
    helpers.has_role("on_call")
    helpers.in_tenant
}

# MFA-based access control

# Require MFA for sensitive operations
allow if {
    input.resource.type == "policies"
    helpers.is_write_operation
    helpers.is_mfa_verified
    helpers.is_admin
    helpers.in_tenant
}

# Require MFA for role assignments
allow if {
    input.resource.type == "roles"
    input.action == "assign"
    helpers.is_mfa_verified
    helpers.is_admin
    helpers.in_tenant
}

# Require MFA for bundle deployments
allow if {
    input.resource.type == "bundles"
    input.action == "deploy"
    helpers.is_mfa_verified
    helpers.is_admin
    helpers.in_tenant
}

# Session age-based restrictions

# Require fresh session for sensitive operations (less than 15 minutes old)
allow if {
    input.resource.type == "tenants"
    input.action == "delete"
    helpers.is_session_fresh(900) # 15 minutes
    helpers.is_super_admin
}

# Require fresh session for user deletions
allow if {
    input.resource.type == "users"
    input.action == "delete"
    helpers.is_session_fresh(900)
    helpers.is_admin
    helpers.in_tenant
}

# Resource attribute-based rules

# Allow access based on resource sensitivity level
allow if {
    input.resource.attributes.sensitivity == "public"
    input.action == "read"
}

# Restrict access to confidential resources
allow if {
    input.resource.attributes.sensitivity == "confidential"
    input.action == "read"
    helpers.has_role("manager")
    helpers.in_tenant
}

# Only specific roles can access restricted resources
allow if {
    input.resource.attributes.sensitivity == "restricted"
    helpers.has_any_role(["admin", "security_officer"])
    helpers.is_mfa_verified
}

# Department-based access
allow if {
    input.resource.attributes.department == input.user.metadata.department
    input.action == "read"
    helpers.in_tenant
}

# Project-based access
allow if {
    project := input.resource.attributes.project
    user_projects := input.user.metadata.projects
    project == user_projects[_]
    helpers.in_tenant
}

# IP-based restrictions

# Restrict admin actions to trusted IPs (if configured)
allow if {
    input.action == "read"
    not helpers.is_write_operation
    helpers.in_tenant
}

# Write operations require trusted network (or override by super admin)
allow if {
    helpers.is_write_operation
    helpers.is_trusted_ip
    helpers.in_tenant
}

# Conditional access based on user metadata

# Users with temporary access
allow if {
    input.user.metadata.temporary_access == true
    temp_expiry := input.user.metadata.access_expiry
    current_time := input.time.timestamp
    current_time < temp_expiry
    input.action == "read"
    helpers.in_tenant
}

# Users with specific clearance levels
allow if {
    required_clearance := input.resource.attributes.required_clearance
    user_clearance := input.user.metadata.clearance_level
    user_clearance >= required_clearance
    helpers.in_tenant
}

# Geo-location based (if available in context)
allow if {
    allowed_regions := input.resource.attributes.allowed_regions
    user_region := input.user.metadata.region
    user_region == allowed_regions[_]
    helpers.in_tenant
}

# Data classification-based access

# Public data - everyone can read
allow if {
    input.resource.attributes.classification == "public"
    input.action == "read"
}

# Internal data - authenticated users in tenant
allow if {
    input.resource.attributes.classification == "internal"
    input.action == "read"
    helpers.in_tenant
}

# Confidential data - requires specific permission
allow if {
    input.resource.attributes.classification == "confidential"
    helpers.has_permission("data.confidential.read")
    helpers.in_tenant
}

# Secret data - requires special role and MFA
allow if {
    input.resource.attributes.classification == "secret"
    helpers.has_role("security_cleared")
    helpers.is_mfa_verified
    helpers.in_tenant
}

# Delegation and approval workflows

# Allow if user has been delegated authority
allow if {
    delegator := input.resource.attributes.delegated_by
    delegator == input.user.id
    delegation_expiry := input.resource.attributes.delegation_expiry
    current_time := input.time.timestamp
    current_time < delegation_expiry
}

# Allow if action has been pre-approved
allow if {
    approval := input.resource.attributes.approvals[_]
    approval.approver_id != input.user.id  # Can't approve your own request
    approval.status == "approved"
    approval.action == input.action
    helpers.in_tenant
}

# Compliance and regulatory rules

# Enforce separation of duties
deny if {
    input.resource.type == "financial_transactions"
    input.action == "approve"
    input.resource.attributes.created_by == input.user.id
}

# Enforce dual control for critical operations
deny if {
    input.resource.type == "bundles"
    input.action == "deploy"
    input.resource.attributes.created_by == input.user.id
    not helpers.is_super_admin
}

# Working hours restrictions for non-critical users
deny if {
    not helpers.is_business_hours
    not helpers.has_any_role(["admin", "on_call", "super_admin"])
    helpers.is_write_operation
    input.resource.type != "audit"  # Allow audit log writes
}

# Deny decisions (explicit denials)
deny if {
    # Block access if user account is suspended
    input.user.metadata.status == "suspended"
}

deny if {
    # Block if user needs password reset
    input.user.metadata.force_password_reset == true
    input.action != "password_reset"
}

deny if {
    # Block if from untrusted location
    input.user.metadata.location_trusted == false
    helpers.is_write_operation
}

# Final decision
default decision = false

decision if {
    allow
    not deny
}
