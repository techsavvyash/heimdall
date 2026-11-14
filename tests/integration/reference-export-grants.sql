GRANT USAGE ON SCHEMA heimdall_managed,heimdall,heimdall_capacity,heimdall_retention TO heimdall_reference_export;
GRANT SELECT(product_id,environment,workspace_id,lifecycle,mutation_blocked,revision,created_at,updated_at)
  ON heimdall_managed.workspaces TO heimdall_reference_export;
GRANT SELECT(product_id,environment,workspace_id,operation_id,kind,state,dispatch_count,generation,next_attempt_at,store_id,model_id,model_hash,manifest_version,created_at,updated_at)
  ON heimdall_managed.operations TO heimdall_reference_export;
GRANT SELECT ON heimdall.schema_metadata,heimdall.migrations,heimdall_managed.schema_metadata,heimdall_managed.migrations,
  heimdall_capacity.policy,heimdall_capacity.upgrade,heimdall_capacity.backfill,
  heimdall_retention.capacity,heimdall_retention.upgrade,heimdall_retention.backfill TO heimdall_reference_export;
