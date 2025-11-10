package heimdall.time_based

import data.heimdall.helpers

# Time-based access control policies

default allow = false

# Business hours access (9 AM - 5 PM, Monday-Friday)
allow {
    helpers.is_business_hours
    helpers.in_tenant
}

# Allow 24/7 access for specific roles
allow {
    helpers.has_any_role(["admin", "super_admin", "on_call", "support"])
    helpers.in_tenant
}

# Weekend access restrictions
deny {
    helpers.is_weekend
    not helpers.has_any_role(["on_call", "admin", "super_admin"])
    helpers.is_write_operation
}

# Night shift access (6 PM - 6 AM)
allow {
    night_shift_hours
    helpers.has_role("night_shift")
    helpers.in_tenant
}

night_shift_hours {
    helpers.in_time_window(18, 24)
}

night_shift_hours {
    helpers.in_time_window(0, 6)
}

# Scheduled maintenance windows
deny {
    in_maintenance_window
    not helpers.is_super_admin
}

in_maintenance_window {
    # Define maintenance windows (e.g., Sunday 2-4 AM)
    input.time.dayOfWeek == "Sunday"
    helpers.in_time_window(2, 4)
}

# Time-window based permissions

# Morning operations (6 AM - 12 PM)
allow {
    helpers.in_time_window(6, 12)
    input.resource.attributes.time_window == "morning"
    helpers.in_tenant
}

# Afternoon operations (12 PM - 6 PM)
allow {
    helpers.in_time_window(12, 18)
    input.resource.attributes.time_window == "afternoon"
    helpers.in_tenant
}

# Evening operations (6 PM - 10 PM)
allow {
    helpers.in_time_window(18, 22)
    input.resource.attributes.time_window == "evening"
    helpers.has_role("evening_staff")
    helpers.in_tenant
}

# Temporary access with expiration

# Check if user has temporary access that hasn't expired
allow {
    user_access_expiry := input.user.metadata.access_expiry
    user_access_expiry != null
    current_time := input.time.timestamp
    current_time < user_access_expiry
    helpers.in_tenant
}

# Role-based temporary access
allow {
    role_expiry := input.user.metadata.role_expiry
    role_expiry != null
    role_expiry[input.user.roles[_]] > input.time.timestamp
    helpers.in_tenant
}

# Scheduled access patterns

# Allow access during scheduled times
allow {
    scheduled_access := input.user.metadata.scheduled_access[_]
    is_in_schedule(scheduled_access)
    helpers.in_tenant
}

is_in_schedule(schedule) {
    # Check day of week
    schedule.days[_] == input.time.dayOfWeek

    # Check time range
    current_hour := input.time.hour
    current_hour >= schedule.start_hour
    current_hour < schedule.end_hour
}

# Resource-specific time restrictions

# Time-locked resources (only accessible during specific hours)
allow {
    time_lock := input.resource.attributes.time_lock
    time_lock != null
    is_within_time_lock(time_lock)
    helpers.in_tenant
}

is_within_time_lock(lock) {
    current_hour := input.time.hour
    current_hour >= lock.start_hour
    current_hour < lock.end_hour
}

# Embargo periods (resource cannot be accessed during specific times)
deny {
    embargo := input.resource.attributes.embargo
    embargo != null
    is_in_embargo_period(embargo)
}

is_in_embargo_period(embargo) {
    current_time := input.time.timestamp
    current_time >= embargo.start_timestamp
    current_time < embargo.end_timestamp
}

# Grace periods for expiring access

# Allow access during grace period after expiration
allow {
    grace_period := input.resource.attributes.grace_period_hours
    grace_period != null
    expiry := input.resource.attributes.expiry_timestamp
    current_time := input.time.timestamp
    grace_end := expiry + (grace_period * 3600)
    current_time < grace_end
    helpers.is_owner
}

# Cooldown periods

# Prevent actions during cooldown period
deny {
    last_action_time := input.resource.attributes.last_action_timestamp
    cooldown_seconds := input.resource.attributes.cooldown_period
    cooldown_seconds != null
    current_time := input.time.timestamp
    (current_time - last_action_time) < cooldown_seconds
}

# Rate limiting based on time

# Limit actions per time window
deny {
    action_count := input.resource.attributes.action_count_today
    max_actions := input.resource.attributes.max_actions_per_day
    max_actions != null
    action_count >= max_actions
}

# Quarterly/Monthly restrictions

# Allow only during specific months
allow {
    allowed_months := input.resource.attributes.allowed_months
    allowed_months != null
    current_month := month_from_timestamp(input.time.timestamp)
    current_month == allowed_months[_]
    helpers.in_tenant
}

# Fiscal year restrictions
allow {
    fiscal_period := input.resource.attributes.fiscal_period
    fiscal_period != null
    is_in_fiscal_period(fiscal_period)
    helpers.in_tenant
}

is_in_fiscal_period(period) {
    current_time := input.time.timestamp
    current_time >= period.start_timestamp
    current_time < period.end_timestamp
}

# Holiday restrictions

# Deny access on holidays (unless override role)
deny {
    is_holiday
    not helpers.has_any_role(["admin", "super_admin", "on_call"])
    helpers.is_write_operation
}

is_holiday {
    # This would typically check against a list of holidays
    # For now, just a placeholder
    input.resource.attributes.is_holiday == true
}

# Deadline-based access

# Deny access after deadline
deny {
    deadline := input.resource.attributes.deadline
    deadline != null
    current_time := input.time.timestamp
    current_time > deadline
    not helpers.is_admin
}

# Allow early access before official start time for privileged users
allow {
    start_time := input.resource.attributes.start_time
    start_time != null
    current_time := input.time.timestamp
    current_time < start_time
    helpers.has_role("early_access")
}

# Session timing

# Require recent authentication for sensitive operations
deny {
    is_sensitive_operation
    session_age := input.context.sessionAge
    max_age := 900  # 15 minutes
    session_age > max_age
}

is_sensitive_operation {
    input.resource.type == "policies"
    helpers.is_write_operation
}

is_sensitive_operation {
    input.resource.type == "roles"
    input.action == "assign"
}

is_sensitive_operation {
    input.resource.type == "bundles"
    input.action == "deploy"
}

# Final decision
default decision = false

decision {
    allow
    not deny
}

# Helper function to extract month from timestamp
month_from_timestamp(ts) = month {
    # This is simplified - in production, you'd use proper date parsing
    # For now, return a placeholder
    month := 1
}
