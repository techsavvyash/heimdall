package heimdall.helpers

# Helper functions used across all policies

# Check if user has a specific role
has_role(role) if {
    role == input.user.roles[_]
}

# Check if user has any of the specified roles
has_any_role(roles) if {
    some role in roles
    has_role(role)
}

# Check if user has all of the specified roles
has_all_roles(roles) if {
    count([r | r := roles[_]; has_role(r)]) == count(roles)
}

# Check if user has a specific permission
has_permission(permission) if {
    permission == input.user.permissions[_]
}

# Check if user has any of the specified permissions
has_any_permission(permissions) if {
    some perm in permissions
    has_permission(perm)
}

# Check if user owns the resource
is_owner if {
    input.user.id == input.resource.ownerId
}

# Check if user belongs to the same tenant as the resource
same_tenant if {
    input.user.tenantId == input.resource.tenantId
}

# Check if user belongs to the same tenant (for general checks)
in_tenant if {
    input.user.tenantId == input.tenant.id
}

# Check if it's business hours (9 AM - 5 PM, Monday-Friday)
is_business_hours if {
    input.time.isBusinessHours == true
}

# Check if it's a weekend
is_weekend if {
    input.time.isWeekend == true
}

# Check if it's within a specific time window
in_time_window(start_hour, end_hour) if {
    input.time.hour >= start_hour
    input.time.hour < end_hour
}

# Check if current hour is within range
is_within_hours(start, end) if {
    hour := input.time.hour
    hour >= start
    hour < end
}

# Check if user has MFA enabled/verified
is_mfa_verified if {
    input.context.mfaVerified == true
}

# Check if request is from a trusted IP (placeholder - implement your logic)
is_trusted_ip if {
    # Add your trusted IP logic here
    true
}

# Check if the action matches
is_action(action) if {
    input.action == action
}

# Check if the resource type matches
is_resource_type(resource_type) if {
    input.resource.type == resource_type
}

# Check if user is admin
is_admin if {
    has_role("admin")
}

# Check if user is super admin
is_super_admin if {
    has_role("super_admin")
}

# Check if session is fresh (less than specified seconds old)
is_session_fresh(max_age_seconds) if {
    input.context.sessionAge < max_age_seconds
}

# Check if it's a read operation
is_read_operation if {
    is_action("read")
}

# Check if it's a write operation
is_write_operation if {
    is_action("create")
} else if {
    is_action("update")
} else if {
    is_action("delete")
}

# Check if user is accessing their own resource
is_self_access if {
    input.user.id == input.resource.id
}

# Build permission string from resource and action
permission_string(resource, action) = perm if {
    perm := sprintf("%s.%s", [resource, action])
}

# Build scoped permission string
permission_string_scoped(resource, action, scope) = perm if {
    perm := sprintf("%s.%s.%s", [resource, action, scope])
}

# Check if user has permission for resource and action
has_resource_permission(resource, action) if {
    perm := permission_string(resource, action)
    has_permission(perm)
}

# Check if user has permission with scope
has_scoped_permission(resource, action, scope) if {
    perm := permission_string_scoped(resource, action, scope)
    has_permission(perm)
}

# Get day of week as number (0 = Sunday, 6 = Saturday)
day_of_week_number := day if {
    input.time.dayOfWeek == "Sunday"
    day := 0
} else = day if {
    input.time.dayOfWeek == "Monday"
    day := 1
} else = day if {
    input.time.dayOfWeek == "Tuesday"
    day := 2
} else = day if {
    input.time.dayOfWeek == "Wednesday"
    day := 3
} else = day if {
    input.time.dayOfWeek == "Thursday"
    day := 4
} else = day if {
    input.time.dayOfWeek == "Friday"
    day := 5
} else = day if {
    input.time.dayOfWeek == "Saturday"
    day := 6
} else = 0
