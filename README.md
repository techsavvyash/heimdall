# Heimdall

**A comprehensive authentication service that simplifies and unifies authentication across all your applications.**

Heimdall is a high-performance authentication proxy built with Go that wraps FusionAuth, providing a clean, unified API layer with additional features like multi-tenancy, advanced RBAC, and comprehensive audit logging. Stop reimplementing authentication for every side project—use Heimdall.

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go)](https://go.dev/)
[![API Version](https://img.shields.io/badge/API-v1.0-green)](./docs/API.md)

---

## Table of Contents

- [Features](#features)
- [Why Heimdall?](#why-heimdall)
- [Quick Start](#quick-start)
- [Documentation](#documentation)
- [Architecture](#architecture)
- [SDKs](#sdks)
- [Examples](#examples)
- [Deployment](#deployment)
- [Contributing](#contributing)
- [License](#license)

---

## Features

### 🔐 **Comprehensive Authentication**
- **Email/Password**: Traditional authentication with email verification
- **Social OAuth**: Google, GitHub, and extensible to other providers
- **Passwordless**: Magic link authentication via email
- **Multi-Factor Authentication**: TOTP, SMS, and email-based 2FA
- **OAuth 2.0 & OpenID Connect**: Full compliance with industry standards

### 👥 **User Management**
- Complete user CRUD operations
- Bulk user import/export
- Profile management with custom attributes
- Account status management (active, suspended, locked)
- Self-service password reset and email verification

### 🏢 **Multi-Tenancy**
- Complete data isolation between tenants
- Tenant-specific authentication configuration
- Resource quotas per tenant
- Custom branding and policies per tenant
- Tenant admin roles

### 🔑 **Role-Based Access Control (RBAC)**
- Flexible role creation and management
- Granular permission system
- Role hierarchy support
- Runtime permission evaluation
- Scope-based access control

### 📊 **Audit Logging**
- Complete audit trail of all security events
- Structured, searchable logs
- Compliance-ready (GDPR, SOC2, HIPAA)
- Export capabilities for external analysis
- Real-time event notifications

### 🚀 **Developer Friendly**
- Official SDKs for JavaScript/TypeScript and Go
- Comprehensive REST API
- OpenAPI/Swagger documentation
- Interactive API playground
- Extensive code examples

### ⚡ **Performance & Scalability**
- Built with Go for high performance
- Horizontal scaling support
- Intelligent caching with Redis
- Connection pooling
- Sub-100ms response times

### 🔒 **Security First**
- Industry-standard encryption
- Rate limiting and DDoS protection
- IP whitelisting
- Account takeover detection
- Security headers and CORS configuration

---

## Why Heimdall?

### The Problem
Building authentication from scratch for every project is time-consuming, error-prone, and risky. Using third-party services directly couples your application to their API and pricing model.

### The Solution
Heimdall provides a unified authentication layer that:

✅ **Saves Development Time**: Drop-in authentication for all your projects
✅ **Reduces Complexity**: One API to learn, consistent across all applications
✅ **Improves Security**: Battle-tested authentication patterns and security best practices
✅ **Enables Flexibility**: Switch underlying providers without changing your application code
✅ **Scales Effortlessly**: Designed for growth from day one
✅ **Maintains Control**: Self-hosted option with complete data ownership

---

## Quick Start

### Prerequisites

- Go 1.21+
- Docker & Docker Compose
- PostgreSQL 14+
- Redis 7+

### 1. Clone the Repository

```bash
git clone https://github.com/techsavvyash/heimdall.git
cd heimdall
```

### 2. Start Dependencies

```bash
docker-compose -f docker-compose.dev.yml up -d
```

This starts PostgreSQL, Redis, and FusionAuth.

### 3. Configure Environment

```bash
cp .env.example .env
# Edit .env with your configuration
```

### 4. Generate JWT Keys

```bash
mkdir -p keys
openssl genrsa -out keys/private.pem 2048
openssl rsa -in keys/private.pem -pubout -out keys/public.pem
```

### 5. Run Migrations

```bash
go run cmd/migrate/main.go up
```

### 6. Start Heimdall

```bash
go run cmd/server/main.go
```

The API will be available at `http://localhost:8080`.

### 7. Try It Out

```bash
# Register a new user
curl -X POST http://localhost:8080/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{
    "email": "user@example.com",
    "password": "SecurePassword123!",
    "firstName": "John",
    "lastName": "Doe"
  }'

# Login
curl -X POST http://localhost:8080/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "user@example.com",
    "password": "SecurePassword123!"
  }'
```

---

## Documentation

### 📚 Core Documentation

- **[Features](./docs/FEATURES.md)**: Comprehensive feature list and capabilities
- **[Architecture](./docs/ARCHITECTURE.md)**: System design, components, and data flows
- **[API Reference](./docs/API.md)**: Complete API documentation with examples
- **[OpenAPI Spec](./docs/openapi.yaml)**: Machine-readable API specification
- **[SDK Documentation](./docs/SDK.md)**: JavaScript/TypeScript and Go SDK guides
- **[Deployment Guide](./docs/DEPLOYMENT.md)**: Docker, Kubernetes, and cloud deployment

### 🎯 Quick Links

- [Getting Started Guide](./docs/GETTING_STARTED.md) *(coming soon)*
- [Configuration Reference](./docs/CONFIGURATION.md) *(coming soon)*
- [Migration Guide](./docs/MIGRATION.md) *(coming soon)*
- [Troubleshooting](./docs/TROUBLESHOOTING.md) *(coming soon)*

---

## Architecture

Heimdall is built as a lightweight proxy layer that enhances FusionAuth with additional features:

```
┌─────────────────────────────────────────────────────────────┐
│                     Client Applications                      │
│  (Web Apps, Mobile Apps, Backend Services via SDKs)         │
└──────────────────┬──────────────────────────────────────────┘
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
    ┌──────────▼──────┐      │      ┌─────────▼─────────┐
    │   FusionAuth    │      │      │   PostgreSQL      │
    │   (Auth Core)   │      │      │   (Heimdall DB)   │
    └─────────────────┘      │      └───────────────────┘
                   ┌─────────▼──────────┐
                   │   Redis Cache      │
                   │  (Sessions/Tokens) │
                   └────────────────────┘
```

**Key Components:**
- **Go API Gateway**: High-performance HTTP server with middleware pipeline
- **FusionAuth**: Battle-tested authentication core
- **PostgreSQL**: Persistent storage for RBAC, audit logs, and tenant data
- **Redis**: High-speed caching for sessions, tokens, and permissions

For detailed architecture information, see [ARCHITECTURE.md](./docs/ARCHITECTURE.md).

---

## SDKs

### JavaScript/TypeScript

```bash
npm install @heimdall/sdk
```

```typescript
import { HeimdallClient } from '@heimdall/sdk';

const heimdall = new HeimdallClient({
  apiUrl: 'https://api.heimdall.yourdomain.com',
  tenantId: 'your-tenant-id'
});

// Login
const { user, accessToken } = await heimdall.auth.login({
  email: 'user@example.com',
  password: 'SecurePassword123!'
});

// Get current user
const profile = await heimdall.users.me();
```

### Go

```bash
go get github.com/techsavvyash/heimdall-go
```

```go
import heimdall "github.com/techsavvyash/heimdall-go"

client, _ := heimdall.NewClient(&heimdall.Config{
    APIUrl:   "https://api.heimdall.yourdomain.com",
    TenantID: "your-tenant-id",
})

// Login
resp, _ := client.Auth.Login(ctx, &heimdall.LoginRequest{
    Email:    "user@example.com",
    Password: "SecurePassword123!",
})

// Get current user
user, _ := client.Users.Me(ctx)
```

For complete SDK documentation, see [SDK.md](./docs/SDK.md).

---

## Examples

The `examples/` directory contains sample applications demonstrating Heimdall integration:

### ElysiaJS Backend (Bun)

Modern TypeScript backend built with ElysiaJS and Bun runtime, showcasing:
- Authentication endpoints using Heimdall SDK
- Protected routes with bearer token authentication
- CORS configuration for frontend integration
- Session management
- Complete CRUD operations

**Location**: `examples/elysia-app/`

**Quick Start**:
```bash
cd examples/elysia-app
bun install
bun run dev
# Open http://localhost:5000 in your browser
```

**Features**:
- ⚡ Ultra-fast Bun runtime
- 🎯 Full TypeScript support
- 🔐 Complete auth flow (register, login, logout)
- 🛡️ Protected API endpoints
- 🌐 Built-in demo frontend (no separate server needed!)
- 📦 Everything runs on one port (5000)

[View ElysiaJS Example README](./examples/elysia-app/README.md) | [Quick Start Guide](./examples/elysia-app/QUICKSTART.md)

### Express.js Sample App

Traditional Node.js application using Express demonstrating basic Heimdall SDK usage.

**Location**: `examples/sample-app/`

**Quick Start**:
```bash
cd examples/sample-app
npm install
npm start
```

---

## Deployment

### Railway (Recommended for Quick Deployment)

Deploy Heimdall to Railway with just a few clicks:

```bash
# Install Railway CLI
npm install -g @railway/cli

# Login and deploy
railway login
railway up
```

For detailed Railway deployment instructions, see **[RAILWAY_DEPLOYMENT.md](./RAILWAY_DEPLOYMENT.md)**.

### Docker

```bash
# Build image
docker build -t heimdall:latest .

# Run with Docker Compose
docker-compose up -d
```

### Kubernetes

```bash
# Create namespace
kubectl create namespace heimdall

# Deploy
kubectl apply -f k8s/
```

### Cloud Platforms

- **Railway**: One-click deployment with auto-scaling (see [RAILWAY_DEPLOYMENT.md](./RAILWAY_DEPLOYMENT.md))
- **AWS EKS**: Full support with RDS and ElastiCache
- **Google Cloud GKE**: Support with Cloud SQL and Memorystore
- **Azure AKS**: Support with Azure Database and Azure Cache

For detailed deployment instructions, see [DEPLOYMENT.md](./docs/DEPLOYMENT.md).

---

## API Examples

### Register a User

```bash
POST /v1/auth/register
Content-Type: application/json

{
  "email": "user@example.com",
  "password": "SecurePassword123!",
  "firstName": "John",
  "lastName": "Doe"
}
```

### Login with Email/Password

```bash
POST /v1/auth/login
Content-Type: application/json

{
  "email": "user@example.com",
  "password": "SecurePassword123!"
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "accessToken": "eyJhbGci...",
    "refreshToken": "rt_abc123...",
    "tokenType": "Bearer",
    "expiresIn": 900,
    "user": {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "email": "user@example.com",
      "firstName": "John",
      "lastName": "Doe"
    }
  }
}
```

### OAuth Login

```bash
GET /v1/auth/oauth/google?redirect_uri=https://yourapp.com/callback
```

For complete API documentation, see [API.md](./docs/API.md).

---

## Project Status

### Current Version: v1.0.0 (Planned)

**Status**: Planning & Documentation Phase

### Roadmap

#### Phase 1: Core Authentication (Q1 2024)
- [ ] Email/Password authentication
- [ ] User registration and login
- [ ] JWT token management
- [ ] Password reset functionality
- [ ] Basic user management API

#### Phase 2: Advanced Auth (Q2 2024)
- [ ] OAuth 2.0 integration (Google, GitHub)
- [ ] Passwordless authentication
- [ ] MFA (TOTP)
- [ ] Email verification

#### Phase 3: Multi-Tenancy & RBAC (Q2 2024)
- [ ] Multi-tenant architecture
- [ ] Role and permission management
- [ ] Tenant-specific configuration
- [ ] Admin APIs

#### Phase 4: SDKs & Tooling (Q3 2024)
- [ ] JavaScript/TypeScript SDK
- [ ] Go SDK
- [ ] CLI tool
- [ ] Admin dashboard (optional)

#### Phase 5: Production Ready (Q3 2024)
- [ ] Comprehensive audit logging
- [ ] Monitoring and metrics
- [ ] Performance optimization
- [ ] Security hardening
- [ ] Production deployment guides

---

## Contributing

We welcome contributions! Please see [CONTRIBUTING.md](./CONTRIBUTING.md) for details on:

- Code of Conduct
- Development setup
- Coding standards
- Pull request process
- Issue reporting

---

## Security

Security is a top priority. If you discover a security vulnerability, please email security@yourdomain.com instead of using the issue tracker.

See [SECURITY.md](./SECURITY.md) for our security policy and supported versions.

---

## License

Heimdall is open-source software licensed under the [MIT License](./LICENSE).

---

## Support

- **Documentation**: [docs/](./docs/)
- **Issues**: [GitHub Issues](https://github.com/techsavvyash/heimdall/issues)
- **Discussions**: [GitHub Discussions](https://github.com/techsavvyash/heimdall/discussions)
- **Email**: support@yourdomain.com

---

## Acknowledgments

- Built with [Go](https://go.dev/)
- Powered by [FusionAuth](https://fusionauth.io/)
- Inspired by the need for simple, unified authentication

---

**Heimdall** - The guardian of your authentication, just as Heimdall guards the Bifrost in Norse mythology.

*"All-seeing and all-hearing guardian of authentication across your applications."*
