package heimdall.authz

import data.heimdall.rbac
import data.heimdall.abac
import data.heimdall.ownership
import data.heimdall.time_based
import data.heimdall.tenant_isolation
import data.heimdall.helpers

# Main authorization entry point
# This policy orchestrates all other policies

# Default deny - security by default
default allow = false

# Main decision logic: allow if ANY policy allows AND tenant isolation is satisfied
allow {
    # Tenant isolation is the most critical check
    tenant_isolation.decision

    # Then check if any of the other policies allow access
    any_policy_allows
}

# Check if any policy allows the action
any_policy_allows {
    rbac.decision
}

any_policy_allows {
    abac.decision
}

any_policy_allows {
    ownership.decision
}

any_policy_allows {
    time_based.decision
}

# Explicit global denials (these override everything)
deny {
    global_deny
}

# Global deny conditions
global_deny {
    # User account is locked
    input.user.metadata.locked == true
}

global_deny {
    # User account is deleted
    input.user.metadata.deleted == true
}

global_deny {
    # IP is blacklisted
    is_blacklisted_ip
}

global_deny {
    # Tenant is suspended
    input.tenant.settings.suspended == true
    not helpers.is_super_admin
}

# System-level protections
global_deny {
    # Cannot delete system resources
    input.action == "delete"
    input.resource.attributes.isSystem == true
    not helpers.is_super_admin
}

global_deny {
    # Cannot modify super admin role
    input.resource.type == "roles"
    input.resource.attributes.name == "super_admin"
    input.action != "read"
    not helpers.is_super_admin
}

# Helper to check if IP is blacklisted
is_blacklisted_ip {
    blacklist := input.tenant.settings.ip_blacklist
    blacklist != null
    input.context.ipAddress == blacklist[_]
}

# Policy evaluation metadata (for debugging and audit)
evaluation_context := {
    "timestamp": input.time.timestamp,
    "user_id": input.user.id,
    "tenant_id": input.tenant.id,
    "resource_type": input.resource.type,
    "resource_id": input.resource.id,
    "action": input.action,
    "policies_evaluated": {
        "rbac": rbac.decision,
        "abac": abac.decision,
        "ownership": ownership.decision,
        "time_based": time_based.decision,
        "tenant_isolation": tenant_isolation.decision
    },
    "final_decision": decision
}

# Final authorization decision
decision {
    allow
    not deny
}

# Detailed decision with reasons (useful for audit/debugging)
decision_details := {
    "allowed": decision,
    "reason": reason,
    "evaluated_policies": policies_evaluated,
    "context": evaluation_context
}

reason := "access_granted" {
    decision
} else := "access_denied"

policies_evaluated := {
    "rbac": {
        "evaluated": true,
        "result": rbac.decision
    },
    "abac": {
        "evaluated": true,
        "result": abac.decision
    },
    "ownership": {
        "evaluated": true,
        "result": ownership.decision
    },
    "time_based": {
        "evaluated": true,
        "result": time_based.decision
    },
    "tenant_isolation": {
        "evaluated": true,
        "result": tenant_isolation.decision
    }
}

# Permission check helpers for common operations

# Can user create resource?
can_create {
    decision
    input.action == "create"
}

# Can user read resource?
can_read {
    decision
    input.action == "read"
}

# Can user update resource?
can_update {
    decision
    input.action == "update"
}

# Can user delete resource?
can_delete {
    decision
    input.action == "delete"
}

# Batch permission check (for checking multiple resources at once)
batch_check[resource_id] = allowed {
    resource_id := input.batch_resources[_].id
    allowed := decision
}
