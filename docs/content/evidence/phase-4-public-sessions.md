---
title: Phase 4 public PostgreSQL session adapters
date: 2026-11-13
---

Products can construct sessions through the public TypeScript and Go clients. The PostgreSQL adapter provides encrypted login and session storage with the configured key ring and key-usage database. The existing core client supplies the scoped identity verifier and durable audit port. Products mount the handlers on their own backend.

```ts
const storage = createPostgresStorage(pool, {
  productId: "ledger",
  environment: "production",
  auditSinks: ["siem"],
  sessions: { keyUsagePool, activeKeyId, keys },
})
const auth = await createHeimdall(configJSON, manifestJSON, {
  policyVersion,
  credentials,
  storage,
}, { signal })
const sessions = await auth.createSessions({
  callbackUrl: "https://ledger.example.com/auth/callback",
  returnTo: "/workspaces",
  zitadelAPIAudience,
}, { signal })
```

```go
storage, err := postgres.NewStorage(db, postgres.StorageOptions{
    ProductID: "support", Environment: "production", AuditSinks: []string{"siem"},
    Sessions: &postgres.SessionStorageOptions{
        KeyUsageDB: keyUsageDB, ActiveKeyID: activeKeyID, Keys: keys,
    },
})
if err != nil { return err }
auth, err := heimdall.New(ctx, configJSON, manifestJSON, heimdall.Options{
    PolicyVersion: policyVersion, Credentials: credentials, Storage: storage,
})
if err != nil { return err }
sessions, err := auth.NewSessions(ctx, heimdall.SessionOptions{
    CallbackURL: "https://support.example.com/auth/callback",
    ReturnTo: "/workspaces", ZitadelAPIAudience: zitadelAPIAudience,
})
if err != nil { return err }
```

Mount Start, Callback and Logout on the corresponding product routes, and call Authenticate in protected handlers. The callback URL must be registered for the configured client in the manifest. The configured return path is relative to the product origin. The product runs the schema migrations, registers the active encryption key with the key-usage ledger, and supplies the exact ZITADEL API audience for its deployment.

The pinned PostgreSQL runner verifies public construction, callback allowlist rejection and a persisted login-start record in both languages with a restricted runtime role. Internal PostgreSQL browser tests cover callback, authentication, refresh and logout. Tenant login policy resolution, custom storage composition and deployed reference product acceptance remain open.
