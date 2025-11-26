# Heimdall Setup Guide

This guide covers the complete setup of Heimdall authentication and authorization service.

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Quick Start](#quick-start)
3. [Development Setup](#development-setup)
4. [Production Deployment](#production-deployment)
5. [Configuration Reference](#configuration-reference)
6. [Database Migrations](#database-migrations)
7. [Verifying the Installation](#verifying-the-installation)

---

## Prerequisites

### System Requirements

- Go 1.21 or later
- Docker and Docker Compose
- OpenSSL (for key generation)
- Make (optional, for build automation)

### Required Services

| Service | Purpose | Default Port |
|---------|---------|--------------|
| PostgreSQL | Primary database | 5433 |
| Redis | Session/cache storage | 6379 |
| FusionAuth | Identity provider | 9011 |
| OPA | Authorization engine | 8181 |
| MinIO | Policy bundle storage | 9000 |

---

## Quick Start

### 1. Clone the Repository

```bash
git clone https://github.com/your-org/heimdall.git
cd heimdall
```

### 2. Start Services

```bash
docker compose up -d
```

### 3. Wait for Services to be Healthy

```bash
docker compose ps
```

All services should show "healthy" status.

### 4. Load OPA Policies

```bash
./load-policies.sh
```

### 5. Verify Installation

```bash
# Check API health
curl http://localhost:8080/health

# Check OPA policies
curl http://localhost:8181/v1/policies
```

### 6. Test Registration

```bash
curl -X POST http://localhost:8080/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{
    "email": "test@example.com",
    "password": "TestPassword123!",
    "firstName": "Test",
    "lastName": "User"
  }'
```

---

## Development Setup

### 1. Environment Configuration

Copy the example environment file:

```bash
cp .env.example .env
```

Edit `.env` with your configuration:

```bash
# Server
PORT=8080
ENVIRONMENT=development

# Database
DB_HOST=localhost
DB_PORT=5433
DB_USER=heimdall
DB_PASSWORD=heimdall_password
DB_NAME=heimdall

# Redis
REDIS_HOST=localhost
REDIS_PORT=6379

# JWT
JWT_PRIVATE_KEY_PATH=./keys/private.pem
JWT_PUBLIC_KEY_PATH=./keys/public.pem
JWT_ACCESS_EXPIRY_MIN=15
JWT_REFRESH_EXPIRY_DAYS=7

# FusionAuth
FUSIONAUTH_URL=http://localhost:9011
FUSIONAUTH_API_KEY=your-api-key
FUSIONAUTH_TENANT_ID=your-tenant-id
FUSIONAUTH_APPLICATION_ID=your-app-id

# OPA
OPA_URL=http://localhost:8181
OPA_POLICY_PATH=heimdall/authz
```

### 2. Generate JWT Keys

```bash
mkdir -p keys
openssl genrsa -out keys/private.pem 2048
openssl rsa -in keys/private.pem -pubout -out keys/public.pem
```

### 3. Start Dependencies

```bash
docker compose up -d postgres redis fusionauth opa minio
```

### 4. Run Migrations

```bash
go run ./cmd/migrate
```

### 5. Load Policies

```bash
./load-policies.sh
```

### 6. Run the Server

```bash
go run ./cmd/server
```

### 7. Run Tests

```bash
# Unit tests
go test ./...

# Integration tests (requires running services)
go test -v ./test/integration/...
```

---

## Production Deployment

### 1. Build the Binary

```bash
CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o server ./cmd/server
CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o migrate ./cmd/migrate
```

### 2. Docker Image

```bash
docker build -t heimdall:latest .
```

### 3. Production Configuration

Create production environment:

```bash
# Required variables for production
ENVIRONMENT=production
PORT=8080

# Database - Use strong credentials
DB_HOST=your-db-host
DB_PORT=5432
DB_USER=heimdall
DB_PASSWORD=<strong-password>
DB_NAME=heimdall
DB_SSLMODE=require
DB_MAX_CONNS=25

# Redis - Enable authentication
REDIS_HOST=your-redis-host
REDIS_PORT=6379
REDIS_PASSWORD=<redis-password>

# JWT - Use secure keys
JWT_PRIVATE_KEY_PATH=/secrets/jwt/private.pem
JWT_PUBLIC_KEY_PATH=/secrets/jwt/public.pem
JWT_ACCESS_EXPIRY_MIN=15
JWT_REFRESH_EXPIRY_DAYS=7

# FusionAuth
FUSIONAUTH_URL=https://your-fusionauth.example.com
FUSIONAUTH_API_KEY=<api-key>
FUSIONAUTH_TENANT_ID=<tenant-id>
FUSIONAUTH_APPLICATION_ID=<app-id>

# OPA
OPA_URL=http://opa:8181
OPA_ENABLE_CACHE=true

# Security
ALLOWED_ORIGINS=https://your-app.example.com
RATE_LIMIT_PER_MIN=100
```

### 4. Kubernetes Deployment

Example deployment manifest:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: heimdall
spec:
  replicas: 3
  selector:
    matchLabels:
      app: heimdall
  template:
    metadata:
      labels:
        app: heimdall
    spec:
      containers:
      - name: heimdall
        image: heimdall:latest
        ports:
        - containerPort: 8080
        envFrom:
        - secretRef:
            name: heimdall-secrets
        - configMapRef:
            name: heimdall-config
        volumeMounts:
        - name: jwt-keys
          mountPath: /secrets/jwt
          readOnly: true
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 10
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 5
      volumes:
      - name: jwt-keys
        secret:
          secretName: jwt-keys
```

### 5. Load Balancer Configuration

Heimdall is stateless (sessions in Redis), so standard round-robin load balancing works.

Health check endpoint: `GET /health`

---

## Configuration Reference

### Server Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | 8080 | Server port |
| `ENVIRONMENT` | development | Environment mode |
| `ALLOWED_ORIGINS` | * | CORS allowed origins |
| `RATE_LIMIT_PER_MIN` | 100 | Global rate limit |

### Database Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_HOST` | localhost | PostgreSQL host |
| `DB_PORT` | 5432 | PostgreSQL port |
| `DB_USER` | heimdall | Database user |
| `DB_PASSWORD` | - | Database password |
| `DB_NAME` | heimdall | Database name |
| `DB_SSLMODE` | disable | SSL mode |
| `DB_MAX_CONNS` | 25 | Max connections |
| `DB_MAX_IDLE` | 5 | Max idle connections |

### Redis Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_HOST` | localhost | Redis host |
| `REDIS_PORT` | 6379 | Redis port |
| `REDIS_PASSWORD` | - | Redis password |
| `REDIS_DB` | 0 | Redis database |

### JWT Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `JWT_PRIVATE_KEY_PATH` | ./keys/private.pem | Private key path |
| `JWT_PUBLIC_KEY_PATH` | ./keys/public.pem | Public key path |
| `JWT_ACCESS_EXPIRY_MIN` | 15 | Access token TTL (minutes) |
| `JWT_REFRESH_EXPIRY_DAYS` | 7 | Refresh token TTL (days) |
| `JWT_ISSUER` | heimdall | Token issuer |

### FusionAuth Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `FUSIONAUTH_URL` | http://localhost:9011 | FusionAuth URL |
| `FUSIONAUTH_API_KEY` | - | API key |
| `FUSIONAUTH_TENANT_ID` | - | Tenant ID |
| `FUSIONAUTH_APPLICATION_ID` | - | Application ID |

### OPA Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `OPA_URL` | http://localhost:8181 | OPA URL |
| `OPA_POLICY_PATH` | heimdall/authz | Policy path |
| `OPA_TIMEOUT_SECONDS` | 5 | Request timeout |
| `OPA_ENABLE_CACHE` | true | Enable Redis cache |

### MinIO Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `MINIO_ENDPOINT` | localhost:9000 | MinIO endpoint |
| `MINIO_ACCESS_KEY` | minioadmin | Access key |
| `MINIO_SECRET_KEY` | minioadmin | Secret key |
| `MINIO_BUCKET` | bundles | Bucket name |
| `MINIO_USE_SSL` | false | Use SSL |

---

## Database Migrations

### Running Migrations

```bash
# Run all pending migrations
go run ./cmd/migrate

# Or use the binary
./migrate
```

### Migration Files

Located in `internal/database/migrations/`:

```
000001_create_tenants.up.sql
000001_create_tenants.down.sql
000002_create_users.up.sql
000002_create_users.down.sql
...
```

### Creating New Migrations

```bash
# Install migrate CLI
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

# Create new migration
migrate create -ext sql -dir internal/database/migrations -seq create_new_table
```

---

## Verifying the Installation

### 1. Health Check

```bash
curl http://localhost:8080/health
```

Expected response:

```json
{"service":"heimdall","status":"healthy","version":"1.0.0"}
```

### 2. OPA Policies

```bash
curl http://localhost:8181/v1/policies | python3 -c "import sys,json; print('Policies:', len(json.load(sys.stdin)['result']))"
```

Expected: `Policies: 7`

### 3. Database Connection

```bash
docker exec -it heimdall-postgres psql -U heimdall -d heimdall -c "SELECT COUNT(*) FROM tenants;"
```

### 4. Redis Connection

```bash
docker exec -it heimdall-redis redis-cli ping
```

Expected: `PONG`

### 5. FusionAuth

```bash
curl http://localhost:9011/api/status
```

### 6. End-to-End Test

```bash
# Register
RESPONSE=$(curl -s -X POST http://localhost:8080/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{
    "email": "e2e-test@example.com",
    "password": "TestPassword123!",
    "firstName": "E2E",
    "lastName": "Test"
  }')

TOKEN=$(echo $RESPONSE | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['accessToken'])")

# Get profile
curl -s http://localhost:8080/v1/users/me \
  -H "Authorization: Bearer $TOKEN"
```

### 7. Run Integration Tests

```bash
go test -v ./test/integration/...
```

All tests should pass.

---

## Troubleshooting

### Service Won't Start

```bash
# Check logs
docker compose logs heimdall

# Common issues:
# - Database not ready: Wait for postgres to be healthy
# - Redis connection: Check REDIS_HOST
# - FusionAuth: Ensure FusionAuth is configured
```

### OPA Policy Errors

```bash
# Check policy syntax
opa check policies/

# Test policy
opa test policies/ -v
```

### JWT Errors

```bash
# Verify keys exist
ls -la keys/

# Test key pair
openssl rsa -in keys/private.pem -check
```

### Database Migration Failures

```bash
# Check migration status
migrate -database "postgres://user:pass@localhost:5433/heimdall?sslmode=disable" -path internal/database/migrations version

# Force version (use with caution)
migrate -database "..." -path internal/database/migrations force VERSION
```

---

## Next Steps

1. Review [Authentication Documentation](./AUTHENTICATION.md)
2. Review [Authorization Documentation](./AUTHORIZATION.md)
3. Configure your client applications with the SDK
4. Set up monitoring and alerting
5. Configure backup for PostgreSQL and Redis
