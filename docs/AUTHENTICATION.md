# Heimdall Authentication Documentation

Heimdall is a high-performance authentication proxy built on Go with FusionAuth as the identity provider. This document covers the authentication system architecture, setup, and usage.

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Prerequisites](#prerequisites)
3. [Configuration](#configuration)
4. [Authentication Endpoints](#authentication-endpoints)
5. [Token Management](#token-management)
6. [Session Management](#session-management)
7. [User Self-Management](#user-self-management)
8. [SDK Usage](#sdk-usage)
9. [Error Handling](#error-handling)

---

## Architecture Overview

Heimdall uses a layered authentication architecture:

```
Client Request
      |
      v
+------------------+
|   Rate Limiter   |  (Redis-backed, IP/User limits)
+------------------+
      |
      v
+------------------+
|  JWT Middleware  |  (RS256 signature validation)
+------------------+
      |
      v
+------------------+
|   Auth Service   |  (Business logic)
+------------------+
      |
      v
+------------------+
|   FusionAuth     |  (Identity provider)
+------------------+
```

### Components

- **JWT Service**: Generates and validates RS256-signed tokens
- **FusionAuth Client**: Handles user credentials and identity management
- **Redis**: Stores refresh tokens, blacklisted tokens, and rate limits
- **PostgreSQL**: Stores user metadata, roles, and audit logs

---

## Prerequisites

### Required Services

| Service | Version | Port | Purpose |
|---------|---------|------|---------|
| PostgreSQL | 13+ | 5433 | Primary database |
| Redis | 6+ | 6379 | Session/cache storage |
| FusionAuth | 1.x+ | 9011 | Identity provider |
| OPA | Latest | 8181 | Authorization policies |
| MinIO | Latest | 9000 | Policy bundle storage |

### Starting Services

```bash
docker compose up -d
```

Verify all services are healthy:

```bash
docker ps --format "table {{.Names}}\t{{.Status}}"
```

---

## Configuration

### Environment Variables

Create a `.env` file or set these environment variables:

```bash
# Server
PORT=8080
ENVIRONMENT=development
ALLOWED_ORIGINS=*
RATE_LIMIT_PER_MIN=100

# Database
DB_HOST=localhost
DB_PORT=5433
DB_USER=heimdall
DB_PASSWORD=heimdall_password
DB_NAME=heimdall
DB_SSLMODE=disable

# Redis
REDIS_HOST=localhost
REDIS_PORT=6379
REDIS_PASSWORD=
REDIS_DB=0

# JWT
JWT_PRIVATE_KEY_PATH=./keys/private.pem
JWT_PUBLIC_KEY_PATH=./keys/public.pem
JWT_ACCESS_EXPIRY_MIN=15
JWT_REFRESH_EXPIRY_DAYS=7
JWT_ISSUER=heimdall

# FusionAuth
FUSIONAUTH_URL=http://localhost:9011
FUSIONAUTH_API_KEY=your-api-key
FUSIONAUTH_TENANT_ID=your-tenant-id
FUSIONAUTH_APPLICATION_ID=your-app-id
```

### Generating JWT Keys

```bash
# Generate private key
openssl genrsa -out keys/private.pem 2048

# Generate public key from private key
openssl rsa -in keys/private.pem -pubout -out keys/public.pem
```

---

## Authentication Endpoints

### Register User

Creates a new user account in both FusionAuth and Heimdall.

```http
POST /v1/auth/register
Content-Type: application/json

{
  "email": "user@example.com",
  "password": "SecurePassword123!",
  "firstName": "John",
  "lastName": "Doe",
  "tenantId": "optional-tenant-uuid"
}
```

**Response (201 Created)**:

```json
{
  "success": true,
  "data": {
    "accessToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
    "refreshToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
    "tokenType": "Bearer",
    "expiresIn": 900,
    "user": {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "email": "user@example.com",
      "firstName": "John",
      "lastName": "Doe",
      "tenantId": "550e8400-e29b-41d4-a716-446655440001"
    }
  }
}
```

**Validation Rules**:
- Email: Required, valid format, unique
- Password: Required, minimum 8 characters
- FirstName: Required
- LastName: Required

### Login

Authenticates a user and returns tokens.

```http
POST /v1/auth/login
Content-Type: application/json

{
  "email": "user@example.com",
  "password": "SecurePassword123!",
  "rememberMe": false
}
```

**Response (200 OK)**:

```json
{
  "success": true,
  "data": {
    "accessToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
    "refreshToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
    "tokenType": "Bearer",
    "expiresIn": 900,
    "user": {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "email": "user@example.com",
      "firstName": "John",
      "lastName": "Doe",
      "tenantId": "550e8400-e29b-41d4-a716-446655440001"
    }
  }
}
```

**Remember Me**: When `rememberMe: true`, refresh token expiry extends to 30 days (default: 7 days).

### Refresh Token

Exchanges a valid refresh token for a new token pair.

```http
POST /v1/auth/refresh
Content-Type: application/json

{
  "refreshToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9..."
}
```

**Response (200 OK)**:

```json
{
  "success": true,
  "data": {
    "accessToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
    "refreshToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
    "tokenType": "Bearer",
    "expiresIn": 900
  }
}
```

The old refresh token is invalidated after use.

### Logout

Invalidates the current session.

```http
POST /v1/auth/logout
Authorization: Bearer <access_token>
```

**Response (200 OK)**:

```json
{
  "success": true,
  "message": "Logged out successfully"
}
```

### Logout All Sessions

Invalidates all sessions for the user.

```http
POST /v1/auth/logout-all
Authorization: Bearer <access_token>
```

**Response (200 OK)**:

```json
{
  "success": true,
  "message": "Logged out from all devices"
}
```

### Change Password

Changes the user's password.

```http
POST /v1/auth/password/change
Authorization: Bearer <access_token>
Content-Type: application/json

{
  "currentPassword": "OldPassword123!",
  "newPassword": "NewSecurePassword456!",
  "confirmPassword": "NewSecurePassword456!"
}
```

**Response (200 OK)**:

```json
{
  "success": true,
  "message": "Password changed successfully"
}
```

---

## Token Management

### Token Structure

Heimdall uses RS256-signed JWTs with the following claims:

**Access Token Claims**:

```json
{
  "userId": "550e8400-e29b-41d4-a716-446655440000",
  "tenantId": "550e8400-e29b-41d4-a716-446655440001",
  "email": "user@example.com",
  "roles": ["user"],
  "type": "access",
  "iss": "heimdall",
  "sub": "550e8400-e29b-41d4-a716-446655440000",
  "exp": 1700000000,
  "nbf": 1699999100,
  "iat": 1699999100,
  "jti": "unique-token-id"
}
```

**Refresh Token Claims**:

```json
{
  "userId": "550e8400-e29b-41d4-a716-446655440000",
  "tenantId": "550e8400-e29b-41d4-a716-446655440001",
  "email": "user@example.com",
  "type": "refresh",
  "iss": "heimdall",
  "sub": "550e8400-e29b-41d4-a716-446655440000",
  "exp": 1700604800,
  "nbf": 1699999100,
  "iat": 1699999100,
  "jti": "unique-token-id"
}
```

### Token Expiry

| Token Type | Default Expiry | With Remember Me |
|------------|----------------|------------------|
| Access | 15 minutes | 15 minutes |
| Refresh | 7 days | 30 days |

### Using Tokens

Include the access token in the Authorization header:

```http
GET /v1/users/me
Authorization: Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...
```

### Token Blacklisting

When a user logs out, the token is blacklisted in Redis for its remaining TTL (max 15 minutes). Subsequent requests with blacklisted tokens are rejected.

---

## Session Management

### Session Storage

Sessions are tracked in Redis with the following key pattern:

```
user:{userId}:{tokenId} -> TTL: token expiry time
blacklist:{tokenId} -> TTL: 15 minutes
```

### Concurrent Sessions

Heimdall supports multiple concurrent sessions per user. Each login creates an independent session that can be revoked individually or all at once.

### Session Age

The session age is calculated from the token's `iat` (issued at) claim and passed to OPA for time-based authorization decisions.

---

## User Self-Management

### Get Profile

```http
GET /v1/users/me
Authorization: Bearer <access_token>
```

**Response**:

```json
{
  "success": true,
  "data": {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "email": "user@example.com",
    "firstName": "John",
    "lastName": "Doe",
    "tenantId": "550e8400-e29b-41d4-a716-446655440001",
    "metadata": {
      "firstName": "John",
      "lastName": "Doe"
    },
    "createdAt": "2024-01-01T00:00:00Z"
  }
}
```

### Update Profile

```http
PATCH /v1/users/me
Authorization: Bearer <access_token>
Content-Type: application/json

{
  "firstName": "Jane",
  "lastName": "Smith",
  "metadata": {
    "preferences": {
      "theme": "dark"
    }
  }
}
```

**Response**:

```json
{
  "success": true,
  "data": {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "email": "user@example.com",
    "firstName": "Jane",
    "lastName": "Smith",
    "tenantId": "550e8400-e29b-41d4-a716-446655440001",
    "metadata": {
      "firstName": "Jane",
      "lastName": "Smith",
      "preferences": {
        "theme": "dark"
      }
    }
  }
}
```

### Delete Account

```http
DELETE /v1/users/me
Authorization: Bearer <access_token>
```

**Response**:

```json
{
  "success": true,
  "message": "Account deleted successfully"
}
```

This soft-deletes the account and removes the user from FusionAuth.

### Get Permissions

```http
GET /v1/users/me/permissions
Authorization: Bearer <access_token>
```

**Response**:

```json
{
  "success": true,
  "data": {
    "permissions": [
      "users:read:own",
      "users:update:own"
    ]
  }
}
```

---

## SDK Usage

### JavaScript/TypeScript

```typescript
import { HeimdallClient } from '@heimdall/sdk';

const client = new HeimdallClient({
  baseUrl: 'http://localhost:8080'
});

// Register
const registerResponse = await client.auth.register({
  email: 'user@example.com',
  password: 'SecurePassword123!',
  firstName: 'John',
  lastName: 'Doe'
});

// Login
const loginResponse = await client.auth.login({
  email: 'user@example.com',
  password: 'SecurePassword123!'
});

// Set token for subsequent requests
client.setToken(loginResponse.data.accessToken);

// Get profile
const profile = await client.users.getMe();

// Update profile
await client.users.updateMe({
  firstName: 'Jane'
});

// Logout
await client.auth.logout();
```

### Go

```go
import "github.com/heimdall/go-sdk"

client := heimdall.NewClient("http://localhost:8080")

// Register
resp, err := client.Auth.Register(&heimdall.RegisterRequest{
    Email:     "user@example.com",
    Password:  "SecurePassword123!",
    FirstName: "John",
    LastName:  "Doe",
})

// Login
loginResp, err := client.Auth.Login(&heimdall.LoginRequest{
    Email:    "user@example.com",
    Password: "SecurePassword123!",
})

// Set token
client.SetToken(loginResp.Data.AccessToken)

// Get profile
profile, err := client.Users.GetMe()

// Logout
err = client.Auth.Logout()
```

---

## Error Handling

### Error Response Format

```json
{
  "success": false,
  "error": {
    "code": "ERROR_CODE",
    "message": "Human-readable error message"
  }
}
```

### Common Error Codes

| Code | HTTP Status | Description |
|------|-------------|-------------|
| `INVALID_REQUEST` | 400 | Malformed request body or invalid parameters |
| `VALIDATION_ERROR` | 400 | Request validation failed |
| `UNAUTHORIZED` | 401 | Missing or invalid authentication |
| `INVALID_CREDENTIALS` | 401 | Wrong email or password |
| `TOKEN_EXPIRED` | 401 | Access token has expired |
| `TOKEN_INVALID` | 401 | Token signature or format invalid |
| `FORBIDDEN` | 403 | User lacks required permissions |
| `USER_EXISTS` | 409 | Email already registered |
| `RATE_LIMITED` | 429 | Too many requests |
| `INTERNAL_ERROR` | 500 | Server error |

### Example Error Responses

**Invalid Credentials**:

```json
{
  "success": false,
  "error": {
    "code": "INVALID_CREDENTIALS",
    "message": "Invalid email or password"
  }
}
```

**Token Expired**:

```json
{
  "success": false,
  "error": {
    "code": "TOKEN_EXPIRED",
    "message": "Access token has expired"
  }
}
```

**Validation Error**:

```json
{
  "success": false,
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "Password must be at least 8 characters"
  }
}
```

---

## Rate Limiting

Heimdall enforces rate limits at two levels:

### Global Rate Limit

- Default: 100 requests per minute per IP
- Configurable via `RATE_LIMIT_PER_MIN`

### Per-User Rate Limit

- Applied after authentication
- Configurable per endpoint

### Rate Limit Headers

```http
X-RateLimit-Limit: 100
X-RateLimit-Remaining: 95
X-RateLimit-Reset: 1699999200
```

### Rate Limit Exceeded Response

```http
HTTP/1.1 429 Too Many Requests
Retry-After: 60

{
  "success": false,
  "error": {
    "code": "RATE_LIMITED",
    "message": "Too many requests, please try again later"
  }
}
```

---

## Security Best Practices

1. **HTTPS**: Always use HTTPS in production
2. **Token Storage**: Store tokens securely (httpOnly cookies or secure storage)
3. **Token Refresh**: Implement automatic token refresh before expiry
4. **Logout on Sensitive Actions**: Force re-authentication for sensitive operations
5. **Password Requirements**: Enforce strong password policies
6. **Rate Limiting**: Configure appropriate rate limits
7. **Audit Logging**: Monitor authentication events
