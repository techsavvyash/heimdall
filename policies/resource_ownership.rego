package heimdall.ownership

import data.heimdall.helpers

# Resource ownership and access control rules

default allow = false

# Users can always read their own resources
allow if {
    helpers.is_owner
    input.action == "read"
}

# Users can update their own resources
allow if {
    helpers.is_owner
    input.action == "update"
    helpers.in_tenant
}

# Users can delete their own resources (unless restricted)
allow if {
    helpers.is_owner
    input.action == "delete"
    helpers.in_tenant
    not is_deletion_restricted
}

# Some resources cannot be deleted by owners
is_deletion_restricted if {
    input.resource.type == "users"
    input.resource.attributes.isSystem == true
}

is_deletion_restricted if {
    input.resource.type == "roles"
    input.resource.attributes.isSystem == true
}

# Owner can share their resources
allow if {
    helpers.is_owner
    input.action == "share"
    helpers.in_tenant
}

# Owner can transfer ownership
allow if {
    helpers.is_owner
    input.action == "transfer_ownership"
    helpers.in_tenant
}

# Shared resource access

# Users can access resources shared with them
allow if {
    shared_with := input.resource.attributes.shared_with[_]
    shared_with.user_id == input.user.id
    input.action == shared_with.permission
    helpers.in_tenant
}

# Users can access resources shared with their team
allow if {
    shared_with := input.resource.attributes.shared_with[_]
    shared_with.team_id == input.user.metadata.team_id
    input.action == shared_with.permission
    helpers.in_tenant
}

# Users can access resources shared with their department
allow if {
    shared_with := input.resource.attributes.shared_with[_]
    shared_with.department == input.user.metadata.department
    input.action == shared_with.permission
    helpers.in_tenant
}

# Manager access to subordinate resources

# Managers can read resources owned by their team members
allow if {
    helpers.has_role("manager")
    owner_team := input.resource.attributes.owner_team
    owner_team == input.user.metadata.team_id
    input.action == "read"
    helpers.in_tenant
}

# Managers can update team member resources
allow if {
    helpers.has_role("manager")
    owner_team := input.resource.attributes.owner_team
    owner_team == input.user.metadata.team_id
    input.action == "update"
    helpers.in_tenant
}

# Hierarchical ownership

# Users can access resources owned by their subordinates
allow if {
    subordinate_ids := input.user.metadata.subordinate_ids
    input.resource.ownerId == subordinate_ids[_]
    input.action == "read"
    helpers.in_tenant
}

# Department heads can access all department resources
allow if {
    helpers.has_role("department_head")
    resource_dept := input.resource.attributes.department
    user_dept := input.user.metadata.department
    resource_dept == user_dept
    helpers.in_tenant
}

# Collaborative resources

# Users can contribute to collaborative resources
allow if {
    input.resource.attributes.collaborative == true
    collaborators := input.resource.attributes.collaborators[_]
    collaborators.user_id == input.user.id
    input.action == collaborators.permission
    helpers.in_tenant
}

# Project-based ownership

# Project members can access project resources
allow if {
    project_id := input.resource.attributes.project_id
    user_projects := input.user.metadata.projects[_]
    user_projects.project_id == project_id
    input.action == user_projects.role_permissions[_]
    helpers.in_tenant
}

# Project owners have full access to project resources
allow if {
    project_id := input.resource.attributes.project_id
    user_projects := input.user.metadata.projects[_]
    user_projects.project_id == project_id
    user_projects.role == "owner"
    helpers.in_tenant
}

# Public resources

# Public resources can be read by anyone in the tenant
allow if {
    input.resource.attributes.visibility == "public"
    input.action == "read"
    helpers.in_tenant
}

# Organization-wide resources can be read by all org members
allow if {
    input.resource.attributes.visibility == "organization"
    input.action == "read"
    helpers.in_tenant
}

# Admin override

# Admins can access any resource in their tenant
allow if {
    helpers.is_admin
    helpers.in_tenant
}

# Super admins can access any resource
allow if {
    helpers.is_super_admin
}

# Audit and compliance

# Auditors can read all resources for compliance
allow if {
    helpers.has_role("auditor")
    input.action == "read"
    helpers.in_tenant
}

# Deny rules

# Cannot access deleted resources (soft delete)
deny if {
    input.resource.attributes.deleted_at != null
    not helpers.is_admin
}

# Cannot access archived resources unless you're the owner or admin
deny if {
    input.resource.attributes.status == "archived"
    not helpers.is_owner
    not helpers.is_admin
}

# Cannot transfer ownership to different tenant
deny if {
    input.action == "transfer_ownership"
    target_tenant := input.resource.attributes.target_tenant_id
    target_tenant != input.user.tenantId
}

# Cannot delete resources that have dependencies
deny if {
    input.action == "delete"
    count(input.resource.attributes.dependencies) > 0
    not helpers.is_admin
}

# Self-access restrictions

# Users cannot delete themselves
deny if {
    input.resource.type == "users"
    input.action == "delete"
    helpers.is_self_access
}

# Users cannot change their own roles
deny if {
    input.resource.type == "users"
    input.action == "update"
    helpers.is_self_access
    input.resource.attributes.field == "roles"
    not helpers.is_super_admin
}

# Users cannot deactivate themselves
deny if {
    input.resource.type == "users"
    input.action == "update"
    helpers.is_self_access
    input.resource.attributes.field == "status"
    input.resource.attributes.new_value == "inactive"
}

# Final decision
default decision = false

decision if {
    allow
    not deny
}

# Helper to check if user is in the owner's team
in_owner_team if {
    owner_team := input.resource.attributes.owner_team
    user_team := input.user.metadata.team_id
    owner_team == user_team
}

# Helper to check if user manages the owner
manages_owner if {
    subordinate_ids := input.user.metadata.subordinate_ids
    input.resource.ownerId == subordinate_ids[_]
}
