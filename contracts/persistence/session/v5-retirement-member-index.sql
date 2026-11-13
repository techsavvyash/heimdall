CREATE INDEX retirement_member_lookup ON heimdall_retention.retirements USING gin (member_event_ids);
