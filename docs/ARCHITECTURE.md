# Heimdall Architecture

## Overview

Heimdall is designed as a lightweight, scalable authentication service that proxies FusionAuth while providing additional features and a unified API layer. Built with Go for high performance and low resource consumption, Heimdall acts as an authentication gateway for all your applications.

## Design Principles

1. **Simplicity**: Easy to deploy, configure, and integrate
2. **Security First**: Security best practices baked in at every layer
3. **Scalability**: Horizontal scaling with stateless design
4. **Multi-tenancy**: Complete isolation between tenants
5. **Developer Friendly**: Clean APIs and comprehensive SDKs
6. **Performance**: Low latency authentication with intelligent caching

## High-Level Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Client Applications                      │
│  (Web Apps, Mobile Apps, Backend Services via SDKs)         │
└──────────────────┬──────────────────────────────────────────┘
                   │
                   │ HTTPS/REST API
                   │
┌──────────────────▼──────────────────────────────────────────┐
│                      Heimdall API Gateway                    │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐            │
│  │   Auth     │  │   User     │  │   Admin    │            │
│  │  Service   │  │   Mgmt     │  │  Service   │            │
│  └────────────┘  └────────────┘  └────────────┘            │
│                                                              │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐            │
│  │   RBAC     │  │   Audit    │  │  Tenant    │            │
│  │  Service   │  │   Logger   │  │   Mgmt     │            │
│  └────────────┘  └────────────┘  └────────────┘            │
└──────────────┬──────────────┬────────────────┬──────────────┘
               │              │                │
               │              │                │
    ┌──────────▼──────┐      │      ┌─────────▼─────────┐
    │   FusionAuth    │      │      │   PostgreSQL      │
    │   (Auth Core)   │      │      │   (Heimdall DB)   │
    └─────────────────┘      │      └───────────────────┘
                             │
                   ┌─────────▼──────────┐
                   │   Redis Cache      │
                   │  (Sessions/Tokens) │
                   └────────────────────┘
```

## Component Architecture

### 1. Heimdall API Gateway (Go)

The core service written in Go that handles all incoming requests and orchestrates authentication flows.

**Responsibilities:**
- Request routing and middleware pipeline
- Authentication and authorization
- Rate limiting and throttling
- Request validation and sanitization
- Response caching
- API versioning

**Technology Stack:**
- **Language**: Go 1.21+
- **Web Framework**: Gin or Chi (lightweight HTTP router)
- **Validation**: Go Validator v10
- **Configuration**: Viper

### 2. Authentication Service

Handles all authentication-related operations.

**Core Functions:**
- Email/password authentication
- OAuth 2.0 provider integration
- Passwordless authentication (magic links)
- MFA enrollment and verification
- Token generation and validation
- Session management

**Flow:**
1. Receive authentication request
2. Validate credentials via FusionAuth
3. Apply tenant-specific policies
4. Generate OAuth 2.0 compliant tokens
5. Store session in Redis
6. Return tokens to client

### 3. User Management Service

Manages user lifecycle and profile operations.

**Core Functions:**
- User CRUD operations
- Profile management
- Account status management
- Email verification workflows
- Password reset workflows
- User search and filtering

**Data Flow:**
- User data primarily stored in FusionAuth
- Additional metadata in Heimdall PostgreSQL
- User sessions cached in Redis

### 4. RBAC Service

Implements role-based access control.

**Core Functions:**
- Role creation and management
- Permission assignment
- Role hierarchy evaluation
- Permission checking
- Scope validation

**Data Model:**
```
Tenant 1---* Role
Role 1---* Permission
User *---* Role (many-to-many)
```

**Storage:**
- Roles and permissions: PostgreSQL
- Permission cache: Redis (TTL-based)

### 5. Audit Logging Service

Tracks all security-relevant events.

**Core Functions:**
- Event capture and logging
- Structured log formatting
- Log querying and filtering
- Retention policy enforcement
- Compliance reporting

**Event Types:**
- Authentication events (login, logout, failures)
- Authorization events (access granted/denied)
- Administrative actions
- User profile changes
- Configuration changes

**Storage:**
- PostgreSQL with time-series optimizations
- Optional integration with external log aggregators (ELK, Datadog)

### 6. Tenant Management Service

Manages multi-tenant configuration and isolation.

**Core Functions:**
- Tenant provisioning
- Tenant configuration management
- Resource quota enforcement
- Tenant-scoped API key management
- Tenant metrics collection

**Isolation Strategy:**
- Database: Separate FusionAuth application per tenant
- Data: Tenant ID in all database records
- Cache: Tenant-prefixed cache keys
- APIs: Tenant ID extraction from JWT or API key

### 7. FusionAuth Integration Layer

Abstracts and enhances FusionAuth functionality.

**Core Functions:**
- FusionAuth API client wrapper
- Request/response transformation
- Error handling and retry logic
- Connection pooling
- Circuit breaker for fault tolerance

**Integration Points:**
- User authentication and registration
- OAuth provider configuration
- Application and tenant mapping
- User profile synchronization

### 8. Data Layer

#### PostgreSQL Database (Heimdall DB)

**Schema:**
```sql
-- Tenants
tenants (
  id UUID PRIMARY KEY,
  name VARCHAR(255),
  fusionauth_app_id UUID,
  settings JSONB,
  created_at TIMESTAMP,
  updated_at TIMESTAMP
)

-- Roles (per tenant)
roles (
  id UUID PRIMARY KEY,
  tenant_id UUID REFERENCES tenants(id),
  name VARCHAR(100),
  description TEXT,
  parent_role_id UUID REFERENCES roles(id),
  created_at TIMESTAMP
)

-- Permissions
permissions (
  id UUID PRIMARY KEY,
  name VARCHAR(100),
  resource VARCHAR(100),
  action VARCHAR(50),
  description TEXT
)

-- Role-Permission mapping
role_permissions (
  role_id UUID REFERENCES roles(id),
  permission_id UUID REFERENCES permissions(id),
  PRIMARY KEY (role_id, permission_id)
)

-- User-Role mapping (supplementary to FusionAuth)
user_roles (
  user_id UUID,
  role_id UUID REFERENCES roles(id),
  tenant_id UUID REFERENCES tenants(id),
  assigned_at TIMESTAMP,
  PRIMARY KEY (user_id, role_id, tenant_id)
)

-- Audit logs
audit_logs (
  id UUID PRIMARY KEY,
  tenant_id UUID REFERENCES tenants(id),
  user_id UUID,
  event_type VARCHAR(50),
  event_category VARCHAR(50),
  resource_type VARCHAR(50),
  resource_id VARCHAR(255),
  ip_address INET,
  user_agent TEXT,
  metadata JSONB,
  created_at TIMESTAMP
)
CREATE INDEX idx_audit_logs_tenant_time ON audit_logs(tenant_id, created_at DESC);
CREATE INDEX idx_audit_logs_user ON audit_logs(user_id, created_at DESC);

-- API Keys
api_keys (
  id UUID PRIMARY KEY,
  tenant_id UUID REFERENCES tenants(id),
  key_hash VARCHAR(255),
  name VARCHAR(100),
  scopes TEXT[],
  expires_at TIMESTAMP,
  last_used_at TIMESTAMP,
  created_at TIMESTAMP
)

-- User metadata (extended attributes)
user_metadata (
  user_id UUID PRIMARY KEY,
  tenant_id UUID REFERENCES tenants(id),
  custom_attributes JSONB,
  preferences JSONB,
  created_at TIMESTAMP,
  updated_at TIMESTAMP
)
```

#### Redis Cache

**Usage:**
- Session storage (key: `session:{session_id}`, TTL: 24h)
- Token blacklist (revoked tokens)
- Permission cache (key: `perms:{user_id}:{tenant_id}`, TTL: 5m)
- Rate limiting counters (key: `ratelimit:{ip}:{endpoint}`, TTL: 1m)
- Temporary data (magic link tokens, OTP codes)

**Configuration:**
- Cluster mode for high availability
- Persistence: RDB snapshots + AOF
- Eviction policy: volatile-ttl

#### FusionAuth Database

**Managed by FusionAuth:**
- User profiles and credentials
- OAuth provider configuration
- Application definitions
- User registrations
- Login records

## Authentication Flows

### 1. Email/Password Login Flow

```
Client                 Heimdall              FusionAuth           Redis
  |                       |                       |                  |
  |-- POST /auth/login -->|                       |                  |
  |  {email, password}    |                       |                  |
  |                       |-- Validate Request -->|                  |
  |                       |                       |                  |
  |                       |<-- User Data ---------|                  |
  |                       |                       |                  |
  |                       |-- Check MFA Required                     |
  |                       |                       |                  |
  |                       |-- Generate Tokens ----|                  |
  |                       |                       |                  |
  |                       |-- Store Session ------|----------------->|
  |                       |                       |                  |
  |                       |-- Log Event (Audit)                      |
  |                       |                       |                  |
  |<-- Tokens + User -----|                       |                  |
  |  {access_token,       |                       |                  |
  |   refresh_token,      |                       |                  |
  |   id_token}           |                       |                  |
```

### 2. OAuth 2.0 Authorization Code Flow

```
Client              Heimdall           FusionAuth        OAuth Provider
  |                    |                    |                  |
  |-- GET /auth/oauth/google --------------->|                  |
  |                    |                    |                  |
  |<-- Redirect to Google -----------------------------redirect-|
  |                    |                    |                  |
  |-- Authorize on Google ----------------------------------->  |
  |                    |                    |                  |
  |<-- Callback with code ------------------------------------ |
  |                    |                    |                  |
  |-- GET /auth/oauth/callback?code=xxx --->|                  |
  |                    |                    |                  |
  |                    |-- Exchange Code -->|                  |
  |                    |                    |-- Get Profile -->|
  |                    |                    |<-- User Data ----|
  |                    |<-- User Record ----|                  |
  |                    |                    |                  |
  |                    |-- Generate Heimdall Tokens            |
  |                    |                    |                  |
  |<-- Tokens ---------|                    |                  |
```

### 3. Passwordless (Magic Link) Flow

```
Client              Heimdall              Database           Email Service
  |                    |                       |                  |
  |-- POST /auth/passwordless --------->       |                  |
  |  {email}           |                       |                  |
  |                    |-- Generate Token      |                  |
  |                    |                       |                  |
  |                    |-- Store Token ------->|                  |
  |                    |                       |                  |
  |                    |-- Send Magic Link ----|----------------->|
  |                    |                       |                  |
  |<-- 200 OK ---------|                       |                  |
  |                    |                       |                  |

  [User clicks link in email]

  |-- GET /auth/verify?token=xxx ------------>|                  |
  |                    |                       |                  |
  |                    |-- Validate Token ---->|                  |
  |                    |<-- Token Valid -------|                  |
  |                    |                       |                  |
  |                    |-- Generate Session Tokens               |
  |                    |                       |                  |
  |<-- Tokens ---------|                       |                  |
```

### 4. MFA Flow (TOTP)

```
Client              Heimdall           FusionAuth          Redis
  |                    |                    |                 |
  |-- POST /auth/login -->                  |                 |
  |  {email, password} |                    |                 |
  |                    |-- Validate ------->|                 |
  |                    |<-- User + MFA Req--|                 |
  |                    |                    |                 |
  |                    |-- Create MFA Session -------------->  |
  |                    |                    |                 |
  |<-- MFA Required ---|                    |                 |
  |  {mfa_token}       |                    |                 |
  |                    |                    |                 |
  |-- POST /auth/mfa/verify --------------> |                 |
  |  {mfa_token, code} |                    |                 |
  |                    |                    |                 |
  |                    |-- Validate TOTP -->|                 |
  |                    |<-- Valid ----------|                 |
  |                    |                    |                 |
  |                    |-- Generate Tokens  |                 |
  |                    |                    |                 |
  |<-- Tokens ---------|                    |                 |
```

## Token Architecture (OAuth 2.0 Compliant)

### Token Types

#### 1. Access Token (JWT)
```json
{
  "header": {
    "alg": "RS256",
    "typ": "JWT",
    "kid": "heimdall-key-1"
  },
  "payload": {
    "iss": "https://heimdall.yourdomain.com",
    "sub": "user-id-uuid",
    "aud": ["app-client-id"],
    "exp": 1735689600,
    "iat": 1735686000,
    "tid": "tenant-id-uuid",
    "scope": "openid profile email",
    "roles": ["user", "admin"],
    "permissions": ["read:users", "write:posts"]
  }
}
```
- **Lifetime**: 15 minutes (configurable)
- **Algorithm**: RS256 or ES256
- **Use**: API authorization
- **Validation**: Signature + expiration

#### 2. Refresh Token
- **Type**: Opaque random string (cryptographically secure)
- **Lifetime**: 30 days (configurable)
- **Storage**: Redis + FusionAuth
- **Rotation**: New refresh token issued on each use
- **Revocation**: Supports immediate revocation

#### 3. ID Token (OpenID Connect)
```json
{
  "iss": "https://heimdall.yourdomain.com",
  "sub": "user-id-uuid",
  "aud": "client-id",
  "exp": 1735689600,
  "iat": 1735686000,
  "name": "John Doe",
  "email": "john@example.com",
  "email_verified": true,
  "picture": "https://...",
  "tid": "tenant-id-uuid"
}
```

### Token Endpoints

- `POST /oauth/token` - Token issuance and refresh
- `POST /oauth/revoke` - Token revocation
- `POST /oauth/introspect` - Token introspection
- `GET /oauth/.well-known/jwks.json` - Public keys for verification

## Security Architecture

### 1. Network Security
- **TLS 1.3**: All communications encrypted
- **HTTPS Only**: Redirect HTTP to HTTPS
- **Certificate Pinning**: Support for mobile apps

### 2. Authentication Security
- **Password Hashing**: bcrypt (FusionAuth handles this)
- **Salted Hashes**: Unique salt per password
- **Password Policies**: Configurable complexity requirements
- **Account Lockout**: Temporary lock after N failed attempts

### 3. Token Security
- **Short-Lived Access Tokens**: 15-minute default
- **Refresh Token Rotation**: New token on each refresh
- **Token Binding**: Bind to client identifier
- **Secure Storage**: HttpOnly cookies for web (recommended)

### 4. API Security
- **Rate Limiting**: Per-IP and per-user limits
- **CORS**: Configurable origin whitelist
- **CSRF Protection**: CSRF tokens for state-changing operations
- **Input Validation**: Strict schema validation
- **SQL Injection Prevention**: Parameterized queries only
- **XSS Prevention**: Output encoding

### 5. Monitoring & Detection
- **Failed Login Monitoring**: Alert on multiple failures
- **Anomaly Detection**: Unusual login patterns
- **IP Reputation**: Block known malicious IPs
- **Audit Logging**: Complete audit trail

## Multi-Tenancy Architecture

### Tenant Isolation Strategy

#### 1. Application Level
- Tenant ID in all database queries
- Middleware enforces tenant context
- API keys scoped to specific tenant

#### 2. Data Level
```
Heimdall DB:
- tenant_id column in all tables
- Row-level security policies (optional)
- Separate indexes per tenant

FusionAuth:
- Separate Application per tenant
- Tenant-specific configuration
```

#### 3. Cache Level
```
Cache key pattern: {tenant_id}:{resource}:{id}

Examples:
- tenant-123:user:user-456
- tenant-123:perms:user-456
- tenant-123:session:session-789
```

### Tenant Provisioning Flow

```
Admin Request --> Heimdall API
                     |
                     v
            1. Create Tenant Record (Heimdall DB)
                     |
                     v
            2. Create FusionAuth Application
                     |
                     v
            3. Configure OAuth Providers (if requested)
                     |
                     v
            4. Create Default Roles (Admin, User)
                     |
                     v
            5. Generate API Keys
                     |
                     v
            6. Return Tenant Configuration
```

## Scalability & Performance

### Horizontal Scaling

**Stateless Design:**
- No server-side state (sessions in Redis)
- Load balancer distributes traffic
- Auto-scaling based on CPU/memory

**Deployment:**
```
┌──────────────┐
│ Load Balancer│ (e.g., nginx, AWS ALB)
└──────┬───────┘
       │
       ├─────────┬─────────┬─────────┐
       │         │         │         │
   ┌───▼──┐  ┌──▼───┐  ┌──▼───┐  ┌──▼───┐
   │Heim-1│  │Heim-2│  │Heim-3│  │Heim-N│
   └──┬───┘  └──┬───┘  └──┬───┘  └──┬───┘
      │         │         │         │
      └─────────┴─────────┴─────────┘
                │
        ┌───────┴────────┐
        │                │
    ┌───▼────┐      ┌───▼──────┐
    │ Redis  │      │PostgreSQL│
    │Cluster │      │ Primary  │
    └────────┘      └─────┬────┘
                          │
                    ┌─────▼────┐
                    │PostgreSQL│
                    │ Replica  │
                    └──────────┘
```

### Caching Strategy

**Layers:**
1. **Application Cache**: In-memory cache for hot data
2. **Redis Cache**: Shared cache across instances
3. **Database Query Cache**: PostgreSQL query cache

**Cache Invalidation:**
- TTL-based expiration
- Event-driven invalidation
- Cache-aside pattern

### Performance Optimizations

- **Connection Pooling**: Reuse database connections
- **Prepared Statements**: Cached query plans
- **Batch Operations**: Bulk user operations
- **Compression**: gzip response compression
- **CDN**: Static assets via CDN
- **Database Indexes**: Strategic indexing

## Technology Stack Summary

| Component | Technology | Purpose |
|-----------|-----------|---------|
| API Server | Go (Gin/Chi) | HTTP server and routing |
| Database | PostgreSQL 14+ | Persistent data storage |
| Cache | Redis 7+ | Session storage, caching |
| Auth Core | FusionAuth | Core authentication engine |
| Message Queue | (Optional) RabbitMQ/Kafka | Async event processing |
| Monitoring | Prometheus + Grafana | Metrics and dashboards |
| Logging | Structured logging (JSON) | Application logs |
| Tracing | OpenTelemetry | Distributed tracing |
| Secrets | HashiCorp Vault / K8s Secrets | Secret management |
| Container | Docker | Containerization |
| Orchestration | Kubernetes / Docker Compose | Container orchestration |

## Deployment Architecture

See [DEPLOYMENT.md](./DEPLOYMENT.md) for detailed deployment configurations and options.

## Data Flow Examples

### User Registration with Email Verification

```
1. Client --> POST /auth/register --> Heimdall
2. Heimdall --> Validate input
3. Heimdall --> Create user in FusionAuth
4. Heimdall --> Store user metadata in PostgreSQL
5. Heimdall --> Generate verification token
6. Heimdall --> Send verification email
7. Heimdall --> Return 201 Created
8. User clicks email link
9. Client --> GET /auth/verify-email?token=xxx --> Heimdall
10. Heimdall --> Validate token
11. Heimdall --> Mark email as verified in FusionAuth
12. Heimdall --> Log audit event
13. Heimdall --> Return success
```

### Permission Check

```
1. Client request with JWT --> Heimdall API
2. Heimdall --> Validate JWT signature
3. Heimdall --> Extract user_id, tenant_id
4. Heimdall --> Check permission cache in Redis
5. If cache miss:
   a. Query roles from PostgreSQL
   b. Query permissions for roles
   c. Cache result in Redis (TTL: 5 min)
6. Heimdall --> Evaluate permission
7. If authorized --> Process request
8. If not authorized --> Return 403 Forbidden
```

## Error Handling & Resilience

### Circuit Breaker Pattern
- Protect FusionAuth integration
- Fallback to cached data when possible
- Graceful degradation

### Retry Logic
- Exponential backoff for transient failures
- Idempotency keys for operations
- Dead letter queue for failed events

### Health Checks
- `/health` - Basic health check
- `/health/ready` - Readiness probe (K8s)
- `/health/live` - Liveness probe (K8s)

## API Versioning

**Strategy**: URL versioning
- Current: `/v1/auth/login`
- Future: `/v2/auth/login`

**Deprecation Policy:**
- 6-month deprecation notice
- Support N and N-1 versions
- Clear migration guides

## Observability

### Metrics (Prometheus)
- Request rate, latency, errors
- Authentication success/failure rates
- Token generation/validation rates
- Cache hit/miss ratios
- Database connection pool stats

### Logging
- Structured JSON logs
- Correlation IDs for request tracing
- Different log levels per environment
- PII redaction in logs

### Tracing
- OpenTelemetry integration
- Distributed tracing across services
- Performance bottleneck identification

## Future Architecture Enhancements

1. **Event-Driven Architecture**: Kafka/RabbitMQ for async processing
2. **CQRS**: Separate read and write models
3. **GraphQL API**: Alternative to REST
4. **Service Mesh**: Istio for advanced traffic management
5. **Database Sharding**: Horizontal partitioning for scale
6. **Global Distribution**: Multi-region deployment
7. **WebAuthn Support**: Biometric authentication
8. **SCIM Server**: Enterprise directory integration
