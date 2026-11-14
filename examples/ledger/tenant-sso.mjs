export function tenantSSOPolicy() {
  const configured = process.env.HEIMDALL_TENANT_SSO_POLICIES;
  if (configured) {
    const policies = new Map(Object.entries(JSON.parse(configured)));
    return async (workspaceId) => policies.get(workspaceId) ?? null;
  }
  if (process.env.HEIMDALL_REFERENCE_STRICT_SSO === "1") {
    return async (workspaceId) =>
      workspaceId === "globex"
        ? {
            organizationId: "reference-globex",
            providerId: "reference-globex-oidc",
            policyVersion: "reference-sso-v1",
            protocol: "oidc",
            maxAgeMs: 900_000,
          }
        : null;
  }
}
