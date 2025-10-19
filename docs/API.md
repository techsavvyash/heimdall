# Heimdall API Documentation

## Overview

Heimdall provides a RESTful API that is OAuth 2.0 and OpenID Connect compliant. All API endpoints use HTTPS and return JSON responses.

**Base URL**: `https://api.heimdall.yourdomain.com/v1`

**Authentication**: Most endpoints require a valid JWT access token in the Authorization header:
```
Authorization: Bearer <access_token>
```

## API Conventions

### Request Format
- **Content-Type**: `application/json`
- **Accept**: `application/json`
- **Tenant Identification**: Via subdomain, header, or JWT claim

### Response Format
All responses follow this structure:

**Success Response:**
```json
{
  "success": true,
  "data": { ... },
  "meta": {
    "requestId": "uuid",
    "timestamp": "2024-01-15T10:30:00Z"
  }
}
```

**Error Response:**
```json
{
  "success": false,
  "error": {
    "code": "ERROR_CODE",
    "message": "Human-readable error message",
    "details": { ... }
  },
  "meta": {
    "requestId": "uuid",
    "timestamp": "2024-01-15T10:30:00Z"
  }
}
```

### HTTP Status Codes
- `200 OK` - Successful request
- `201 Created` - Resource created successfully
- `204 No Content` - Successful request with no response body
- `400 Bad Request` - Invalid request parameters
- `401 Unauthorized` - Missing or invalid authentication
- `403 Forbidden` - Insufficient permissions
- `404 Not Found` - Resource not found
- `409 Conflict` - Resource conflict (e.g., email already exists)
- `429 Too Many Requests` - Rate limit exceeded
- `500 Internal Server Error` - Server error
- `503 Service Unavailable` - Service temporarily unavailable

### Pagination
List endpoints support pagination:

**Request:**
```
GET /v1/users?page=1&limit=20&sort=created_at:desc
```

**Response:**
```json
{
  "success": true,
  "data": [ ... ],
  "meta": {
    "pagination": {
      "page": 1,
      "limit": 20,
      "total": 150,
      "totalPages": 8
    }
  }
}
```

### Rate Limiting
Rate limits are enforced per tenant and per user:
- **Authenticated requests**: 1000 requests per hour
- **Unauthenticated requests**: 100 requests per hour

Response headers:
```
X-RateLimit-Limit: 1000
X-RateLimit-Remaining: 950
X-RateLimit-Reset: 1735689600
```

---

## Authentication Endpoints

### 1. Register User

Create a new user account.

**Endpoint:** `POST /v1/auth/register`

**Authentication:** None

**Request Body:**
```json
{
  "email": "user@example.com",
  "password": "SecurePassword123!",
  "firstName": "John",
  "lastName": "Doe",
  "phoneNumber": "+1234567890",
  "metadata": {
    "source": "web",
    "referralCode": "ABC123"
  }
}
```

**Response:** `201 Created`
```json
{
  "success": true,
  "data": {
    "userId": "550e8400-e29b-41d4-a716-446655440000",
    "email": "user@example.com",
    "emailVerified": false,
    "firstName": "John",
    "lastName": "Doe",
    "createdAt": "2024-01-15T10:30:00Z"
  }
}
```

**Errors:**
- `409 Conflict` - Email already exists
- `400 Bad Request` - Invalid input (weak password, invalid email)

---

### 2. Login (Email/Password)

Authenticate with email and password.

**Endpoint:** `POST /v1/auth/login`

**Authentication:** None

**Request Body:**
```json
{
  "email": "user@example.com",
  "password": "SecurePassword123!",
  "rememberMe": true
}
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "accessToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
    "refreshToken": "rt_0a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p",
    "idToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
    "tokenType": "Bearer",
    "expiresIn": 900,
    "user": {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "email": "user@example.com",
      "firstName": "John",
      "lastName": "Doe",
      "emailVerified": true,
      "roles": ["user"]
    },
    "mfaRequired": false
  }
}
```

**MFA Response:** `200 OK` (when MFA is enabled)
```json
{
  "success": true,
  "data": {
    "mfaRequired": true,
    "mfaToken": "mfa_temp_token_xyz",
    "methods": ["totp", "sms", "email"]
  }
}
```

**Errors:**
- `401 Unauthorized` - Invalid credentials
- `423 Locked` - Account locked due to too many failed attempts

---

### 3. Login with OAuth

Initiate OAuth login flow.

**Endpoint:** `GET /v1/auth/oauth/{provider}`

**Parameters:**
- `provider`: `google`, `github`, `facebook`, etc.

**Query Parameters:**
```
redirect_uri=https://yourapp.com/callback
state=random_state_string
```

**Response:** `302 Redirect`
Redirects to OAuth provider's authorization page.

---

### 4. OAuth Callback

Handle OAuth provider callback.

**Endpoint:** `GET /v1/auth/oauth/callback`

**Query Parameters:**
```
code=authorization_code
state=random_state_string
```

**Response:** `302 Redirect`
Redirects to application with tokens in URL fragment or query parameters.

---

### 5. Passwordless Login (Magic Link)

Request a magic link for passwordless authentication.

**Endpoint:** `POST /v1/auth/passwordless`

**Authentication:** None

**Request Body:**
```json
{
  "email": "user@example.com",
  "redirectUri": "https://yourapp.com/auth/callback"
}
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "message": "Magic link sent to your email",
    "expiresIn": 900
  }
}
```

---

### 6. Verify Magic Link

Verify magic link token.

**Endpoint:** `GET /v1/auth/passwordless/verify`

**Query Parameters:**
```
token=magic_link_token
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "accessToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
    "refreshToken": "rt_0a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p",
    "idToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
    "tokenType": "Bearer",
    "expiresIn": 900,
    "user": { ... }
  }
}
```

**Errors:**
- `401 Unauthorized` - Invalid or expired token

---

### 7. MFA: Enroll TOTP

Enroll in TOTP-based MFA.

**Endpoint:** `POST /v1/auth/mfa/totp/enroll`

**Authentication:** Required

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "secret": "JBSWY3DPEHPK3PXP",
    "qrCode": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAA...",
    "recoveryCodes": [
      "1234-5678-9012",
      "3456-7890-1234",
      "5678-9012-3456"
    ]
  }
}
```

---

### 8. MFA: Verify TOTP

Verify TOTP code to complete enrollment or during login.

**Endpoint:** `POST /v1/auth/mfa/totp/verify`

**Authentication:** Required (or MFA token)

**Request Body:**
```json
{
  "code": "123456",
  "mfaToken": "mfa_temp_token_xyz"
}
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "accessToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
    "refreshToken": "rt_0a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p",
    "idToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
    "tokenType": "Bearer",
    "expiresIn": 900,
    "user": { ... }
  }
}
```

**Errors:**
- `401 Unauthorized` - Invalid code

---

### 9. Logout

Invalidate current session and tokens.

**Endpoint:** `POST /v1/auth/logout`

**Authentication:** Required

**Request Body:**
```json
{
  "refreshToken": "rt_0a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p"
}
```

**Response:** `204 No Content`

---

### 10. Refresh Token

Obtain a new access token using refresh token.

**Endpoint:** `POST /v1/auth/refresh`

**Authentication:** None

**Request Body:**
```json
{
  "refreshToken": "rt_0a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p"
}
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "accessToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
    "refreshToken": "rt_9p0o1n2m3l4k5j6i7h8g9f0e1d2c3b4a",
    "tokenType": "Bearer",
    "expiresIn": 900
  }
}
```

**Errors:**
- `401 Unauthorized` - Invalid or expired refresh token

---

### 11. Verify Email

Verify user's email address.

**Endpoint:** `GET /v1/auth/verify-email`

**Query Parameters:**
```
token=email_verification_token
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "message": "Email verified successfully",
    "emailVerified": true
  }
}
```

---

### 12. Resend Verification Email

Resend email verification.

**Endpoint:** `POST /v1/auth/verify-email/resend`

**Authentication:** Required

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "message": "Verification email sent"
  }
}
```

---

### 13. Request Password Reset

Request password reset link.

**Endpoint:** `POST /v1/auth/password/reset`

**Authentication:** None

**Request Body:**
```json
{
  "email": "user@example.com"
}
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "message": "Password reset link sent to your email"
  }
}
```

---

### 14. Reset Password

Reset password with token.

**Endpoint:** `POST /v1/auth/password/reset/confirm`

**Authentication:** None

**Request Body:**
```json
{
  "token": "password_reset_token",
  "newPassword": "NewSecurePassword123!"
}
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "message": "Password reset successfully"
  }
}
```

---

### 15. Change Password

Change password for authenticated user.

**Endpoint:** `POST /v1/auth/password/change`

**Authentication:** Required

**Request Body:**
```json
{
  "currentPassword": "OldPassword123!",
  "newPassword": "NewSecurePassword123!"
}
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "message": "Password changed successfully"
  }
}
```

**Errors:**
- `401 Unauthorized` - Incorrect current password

---

## User Management Endpoints

### 16. Get Current User

Get authenticated user's profile.

**Endpoint:** `GET /v1/users/me`

**Authentication:** Required

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "email": "user@example.com",
    "emailVerified": true,
    "firstName": "John",
    "lastName": "Doe",
    "phoneNumber": "+1234567890",
    "profilePicture": "https://...",
    "roles": ["user", "admin"],
    "permissions": ["read:users", "write:posts"],
    "metadata": {
      "source": "web"
    },
    "createdAt": "2024-01-01T00:00:00Z",
    "updatedAt": "2024-01-15T10:30:00Z",
    "lastLoginAt": "2024-01-15T09:00:00Z"
  }
}
```

---

### 17. Update Current User

Update authenticated user's profile.

**Endpoint:** `PATCH /v1/users/me`

**Authentication:** Required

**Request Body:**
```json
{
  "firstName": "John",
  "lastName": "Smith",
  "phoneNumber": "+1234567890",
  "profilePicture": "https://...",
  "metadata": {
    "preferences": {
      "theme": "dark"
    }
  }
}
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "email": "user@example.com",
    "firstName": "John",
    "lastName": "Smith",
    ...
  }
}
```

---

### 18. List Users (Admin)

List all users in tenant.

**Endpoint:** `GET /v1/users`

**Authentication:** Required (admin role)

**Query Parameters:**
```
page=1
limit=20
sort=created_at:desc
search=john
role=admin
status=active
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "email": "user@example.com",
      "firstName": "John",
      "lastName": "Doe",
      "emailVerified": true,
      "roles": ["user"],
      "status": "active",
      "createdAt": "2024-01-01T00:00:00Z"
    }
  ],
  "meta": {
    "pagination": {
      "page": 1,
      "limit": 20,
      "total": 150,
      "totalPages": 8
    }
  }
}
```

---

### 19. Get User by ID (Admin)

Get specific user's profile.

**Endpoint:** `GET /v1/users/{userId}`

**Authentication:** Required (admin role)

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "email": "user@example.com",
    ...
  }
}
```

**Errors:**
- `404 Not Found` - User not found

---

### 20. Create User (Admin)

Create a new user.

**Endpoint:** `POST /v1/users`

**Authentication:** Required (admin role)

**Request Body:**
```json
{
  "email": "newuser@example.com",
  "password": "SecurePassword123!",
  "firstName": "Jane",
  "lastName": "Doe",
  "emailVerified": true,
  "roles": ["user"],
  "sendVerificationEmail": false
}
```

**Response:** `201 Created`
```json
{
  "success": true,
  "data": {
    "id": "660e8400-e29b-41d4-a716-446655440001",
    "email": "newuser@example.com",
    ...
  }
}
```

---

### 21. Update User (Admin)

Update user's profile.

**Endpoint:** `PATCH /v1/users/{userId}`

**Authentication:** Required (admin role)

**Request Body:**
```json
{
  "firstName": "Jane",
  "lastName": "Smith",
  "emailVerified": true,
  "status": "active"
}
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    ...
  }
}
```

---

### 22. Delete User (Admin)

Delete a user account.

**Endpoint:** `DELETE /v1/users/{userId}`

**Authentication:** Required (admin role)

**Query Parameters:**
```
hardDelete=false
```

**Response:** `204 No Content`

---

### 23. Bulk Import Users (Admin)

Import multiple users.

**Endpoint:** `POST /v1/users/import`

**Authentication:** Required (admin role)

**Request Body:**
```json
{
  "users": [
    {
      "email": "user1@example.com",
      "firstName": "User",
      "lastName": "One",
      "roles": ["user"]
    },
    {
      "email": "user2@example.com",
      "firstName": "User",
      "lastName": "Two",
      "roles": ["user"]
    }
  ],
  "sendVerificationEmails": true
}
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "imported": 2,
    "failed": 0,
    "errors": []
  }
}
```

---

## RBAC Endpoints

### 24. List Roles

Get all roles in tenant.

**Endpoint:** `GET /v1/roles`

**Authentication:** Required (admin role)

**Response:** `200 OK`
```json
{
  "success": true,
  "data": [
    {
      "id": "role-id-1",
      "name": "admin",
      "description": "Administrator role with full access",
      "permissions": ["*"],
      "parentRoleId": null,
      "createdAt": "2024-01-01T00:00:00Z"
    },
    {
      "id": "role-id-2",
      "name": "user",
      "description": "Standard user role",
      "permissions": ["read:own_profile", "write:own_profile"],
      "parentRoleId": null,
      "createdAt": "2024-01-01T00:00:00Z"
    }
  ]
}
```

---

### 25. Create Role

Create a new role.

**Endpoint:** `POST /v1/roles`

**Authentication:** Required (admin role)

**Request Body:**
```json
{
  "name": "editor",
  "description": "Content editor role",
  "permissions": ["read:posts", "write:posts", "delete:own_posts"],
  "parentRoleId": "role-id-2"
}
```

**Response:** `201 Created`
```json
{
  "success": true,
  "data": {
    "id": "role-id-3",
    "name": "editor",
    "description": "Content editor role",
    "permissions": ["read:posts", "write:posts", "delete:own_posts"],
    "parentRoleId": "role-id-2",
    "createdAt": "2024-01-15T10:30:00Z"
  }
}
```

---

### 26. Update Role

Update an existing role.

**Endpoint:** `PATCH /v1/roles/{roleId}`

**Authentication:** Required (admin role)

**Request Body:**
```json
{
  "description": "Updated description",
  "permissions": ["read:posts", "write:posts", "delete:posts"]
}
```

**Response:** `200 OK`

---

### 27. Delete Role

Delete a role.

**Endpoint:** `DELETE /v1/roles/{roleId}`

**Authentication:** Required (admin role)

**Response:** `204 No Content`

---

### 28. Assign Role to User

Assign role(s) to a user.

**Endpoint:** `POST /v1/users/{userId}/roles`

**Authentication:** Required (admin role)

**Request Body:**
```json
{
  "roleIds": ["role-id-2", "role-id-3"]
}
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "userId": "550e8400-e29b-41d4-a716-446655440000",
    "roles": ["user", "editor"]
  }
}
```

---

### 29. Remove Role from User

Remove role from a user.

**Endpoint:** `DELETE /v1/users/{userId}/roles/{roleId}`

**Authentication:** Required (admin role)

**Response:** `204 No Content`

---

### 30. Check Permission

Check if user has specific permission.

**Endpoint:** `GET /v1/users/{userId}/permissions/check`

**Authentication:** Required

**Query Parameters:**
```
permission=write:posts
resource=post-123
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "hasPermission": true,
    "grantedBy": ["editor"]
  }
}
```

---

## Tenant Management Endpoints

### 31. Get Current Tenant

Get current tenant information.

**Endpoint:** `GET /v1/tenants/current`

**Authentication:** Required

**Response:** `200 OK`
```json
{
  "success": true,
  "data": {
    "id": "tenant-id-123",
    "name": "Acme Corp",
    "domain": "acme.heimdall.io",
    "settings": {
      "authMethods": ["email", "google", "github"],
      "mfaRequired": false,
      "passwordPolicy": {
        "minLength": 8,
        "requireUppercase": true,
        "requireNumbers": true,
        "requireSpecialChars": true
      },
      "tokenLifetimes": {
        "accessToken": 900,
        "refreshToken": 2592000
      }
    },
    "quotas": {
      "maxUsers": 10000,
      "apiCallsPerHour": 100000
    },
    "createdAt": "2024-01-01T00:00:00Z"
  }
}
```

---

### 32. Update Tenant Settings (Admin)

Update tenant configuration.

**Endpoint:** `PATCH /v1/tenants/{tenantId}`

**Authentication:** Required (tenant admin)

**Request Body:**
```json
{
  "name": "Acme Corporation",
  "settings": {
    "mfaRequired": true,
    "passwordPolicy": {
      "minLength": 10
    }
  }
}
```

**Response:** `200 OK`

---

### 33. Create Tenant (Super Admin)

Create a new tenant.

**Endpoint:** `POST /v1/tenants`

**Authentication:** Required (super admin)

**Request Body:**
```json
{
  "name": "New Corp",
  "adminEmail": "admin@newcorp.com",
  "settings": {
    "authMethods": ["email", "google"]
  }
}
```

**Response:** `201 Created`
```json
{
  "success": true,
  "data": {
    "id": "tenant-id-456",
    "name": "New Corp",
    "apiKey": "hdk_live_abc123xyz789",
    "adminUser": {
      "id": "user-id-789",
      "email": "admin@newcorp.com",
      "temporaryPassword": "TempPass123!"
    },
    "createdAt": "2024-01-15T10:30:00Z"
  }
}
```

---

## Audit Log Endpoints

### 34. Query Audit Logs

Get audit logs with filtering.

**Endpoint:** `GET /v1/audit-logs`

**Authentication:** Required (admin role)

**Query Parameters:**
```
page=1
limit=50
userId=550e8400-e29b-41d4-a716-446655440000
eventType=auth.login
eventCategory=authentication
startDate=2024-01-01T00:00:00Z
endDate=2024-01-15T23:59:59Z
```

**Response:** `200 OK`
```json
{
  "success": true,
  "data": [
    {
      "id": "log-id-1",
      "tenantId": "tenant-id-123",
      "userId": "550e8400-e29b-41d4-a716-446655440000",
      "eventType": "auth.login",
      "eventCategory": "authentication",
      "resourceType": "user",
      "resourceId": "550e8400-e29b-41d4-a716-446655440000",
      "ipAddress": "192.168.1.1",
      "userAgent": "Mozilla/5.0...",
      "metadata": {
        "method": "email",
        "success": true
      },
      "createdAt": "2024-01-15T10:30:00Z"
    }
  ],
  "meta": {
    "pagination": {
      "page": 1,
      "limit": 50,
      "total": 1234,
      "totalPages": 25
    }
  }
}
```

---

### 35. Export Audit Logs

Export audit logs as CSV or JSON.

**Endpoint:** `GET /v1/audit-logs/export`

**Authentication:** Required (admin role)

**Query Parameters:**
```
format=csv
startDate=2024-01-01T00:00:00Z
endDate=2024-01-15T23:59:59Z
```

**Response:** `200 OK`
- Content-Type: `text/csv` or `application/json`
- File download

---

## OAuth 2.0 / OpenID Connect Endpoints

### 36. Authorization Endpoint

OAuth 2.0 authorization endpoint.

**Endpoint:** `GET /v1/oauth/authorize`

**Query Parameters:**
```
response_type=code
client_id=your_client_id
redirect_uri=https://yourapp.com/callback
scope=openid profile email
state=random_state
code_challenge=challenge
code_challenge_method=S256
```

**Response:** `302 Redirect`
Redirects to login page, then back to redirect_uri with authorization code.

---

### 37. Token Endpoint

Exchange authorization code for tokens.

**Endpoint:** `POST /v1/oauth/token`

**Request Body (Authorization Code):**
```json
{
  "grant_type": "authorization_code",
  "code": "authorization_code",
  "redirect_uri": "https://yourapp.com/callback",
  "client_id": "your_client_id",
  "client_secret": "your_client_secret",
  "code_verifier": "verifier"
}
```

**Request Body (Refresh Token):**
```json
{
  "grant_type": "refresh_token",
  "refresh_token": "rt_0a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p",
  "client_id": "your_client_id",
  "client_secret": "your_client_secret"
}
```

**Request Body (Client Credentials):**
```json
{
  "grant_type": "client_credentials",
  "client_id": "your_client_id",
  "client_secret": "your_client_secret",
  "scope": "api:read api:write"
}
```

**Response:** `200 OK`
```json
{
  "access_token": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
  "token_type": "Bearer",
  "expires_in": 900,
  "refresh_token": "rt_0a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p",
  "id_token": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
  "scope": "openid profile email"
}
```

---

### 38. Token Revocation

Revoke access or refresh token.

**Endpoint:** `POST /v1/oauth/revoke`

**Request Body:**
```json
{
  "token": "token_to_revoke",
  "token_type_hint": "refresh_token",
  "client_id": "your_client_id",
  "client_secret": "your_client_secret"
}
```

**Response:** `200 OK`

---

### 39. Token Introspection

Inspect token validity and claims.

**Endpoint:** `POST /v1/oauth/introspect`

**Request Body:**
```json
{
  "token": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
  "client_id": "your_client_id",
  "client_secret": "your_client_secret"
}
```

**Response:** `200 OK`
```json
{
  "active": true,
  "scope": "openid profile email",
  "client_id": "your_client_id",
  "username": "user@example.com",
  "token_type": "Bearer",
  "exp": 1735689600,
  "iat": 1735686000,
  "sub": "550e8400-e29b-41d4-a716-446655440000",
  "aud": ["your_client_id"],
  "iss": "https://heimdall.yourdomain.com"
}
```

---

### 40. UserInfo Endpoint

Get user information (OpenID Connect).

**Endpoint:** `GET /v1/oauth/userinfo`

**Authentication:** Required (access token)

**Response:** `200 OK`
```json
{
  "sub": "550e8400-e29b-41d4-a716-446655440000",
  "email": "user@example.com",
  "email_verified": true,
  "name": "John Doe",
  "given_name": "John",
  "family_name": "Doe",
  "picture": "https://...",
  "updated_at": 1735686000
}
```

---

### 41. JWKS Endpoint

Get JSON Web Key Set for token validation.

**Endpoint:** `GET /v1/oauth/.well-known/jwks.json`

**Authentication:** None

**Response:** `200 OK`
```json
{
  "keys": [
    {
      "kty": "RSA",
      "use": "sig",
      "kid": "heimdall-key-1",
      "alg": "RS256",
      "n": "0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx...",
      "e": "AQAB"
    }
  ]
}
```

---

### 42. OpenID Configuration

Get OpenID Connect discovery document.

**Endpoint:** `GET /v1/.well-known/openid-configuration`

**Authentication:** None

**Response:** `200 OK`
```json
{
  "issuer": "https://heimdall.yourdomain.com",
  "authorization_endpoint": "https://heimdall.yourdomain.com/v1/oauth/authorize",
  "token_endpoint": "https://heimdall.yourdomain.com/v1/oauth/token",
  "userinfo_endpoint": "https://heimdall.yourdomain.com/v1/oauth/userinfo",
  "jwks_uri": "https://heimdall.yourdomain.com/v1/oauth/.well-known/jwks.json",
  "revocation_endpoint": "https://heimdall.yourdomain.com/v1/oauth/revoke",
  "introspection_endpoint": "https://heimdall.yourdomain.com/v1/oauth/introspect",
  "response_types_supported": ["code", "token", "id_token"],
  "subject_types_supported": ["public"],
  "id_token_signing_alg_values_supported": ["RS256"],
  "scopes_supported": ["openid", "profile", "email", "offline_access"],
  "token_endpoint_auth_methods_supported": ["client_secret_basic", "client_secret_post"],
  "claims_supported": ["sub", "email", "email_verified", "name", "given_name", "family_name", "picture"]
}
```

---

## Webhooks Endpoints

### 43. List Webhooks

Get all configured webhooks.

**Endpoint:** `GET /v1/webhooks`

**Authentication:** Required (admin role)

**Response:** `200 OK`
```json
{
  "success": true,
  "data": [
    {
      "id": "webhook-id-1",
      "url": "https://yourapp.com/webhooks/auth",
      "events": ["user.created", "user.login"],
      "active": true,
      "secret": "whsec_abc123",
      "createdAt": "2024-01-01T00:00:00Z"
    }
  ]
}
```

---

### 44. Create Webhook

Create a new webhook.

**Endpoint:** `POST /v1/webhooks`

**Authentication:** Required (admin role)

**Request Body:**
```json
{
  "url": "https://yourapp.com/webhooks/auth",
  "events": ["user.created", "user.login", "user.logout"],
  "active": true
}
```

**Response:** `201 Created`
```json
{
  "success": true,
  "data": {
    "id": "webhook-id-2",
    "url": "https://yourapp.com/webhooks/auth",
    "events": ["user.created", "user.login", "user.logout"],
    "active": true,
    "secret": "whsec_xyz789",
    "createdAt": "2024-01-15T10:30:00Z"
  }
}
```

---

### 45. Delete Webhook

Delete a webhook.

**Endpoint:** `DELETE /v1/webhooks/{webhookId}`

**Authentication:** Required (admin role)

**Response:** `204 No Content`

---

## Health & Monitoring Endpoints

### 46. Health Check

Basic health check.

**Endpoint:** `GET /health`

**Authentication:** None

**Response:** `200 OK`
```json
{
  "status": "healthy",
  "timestamp": "2024-01-15T10:30:00Z"
}
```

---

### 47. Readiness Check

Kubernetes readiness probe.

**Endpoint:** `GET /health/ready`

**Authentication:** None

**Response:** `200 OK` or `503 Service Unavailable`

---

### 48. Liveness Check

Kubernetes liveness probe.

**Endpoint:** `GET /health/live`

**Authentication:** None

**Response:** `200 OK` or `503 Service Unavailable`

---

## Error Codes Reference

| Code | Description |
|------|-------------|
| `VALIDATION_ERROR` | Request validation failed |
| `UNAUTHORIZED` | Authentication required or failed |
| `FORBIDDEN` | Insufficient permissions |
| `NOT_FOUND` | Resource not found |
| `CONFLICT` | Resource already exists |
| `RATE_LIMIT_EXCEEDED` | Too many requests |
| `INVALID_CREDENTIALS` | Invalid email or password |
| `ACCOUNT_LOCKED` | Account temporarily locked |
| `EMAIL_NOT_VERIFIED` | Email verification required |
| `MFA_REQUIRED` | Multi-factor authentication required |
| `INVALID_TOKEN` | Token is invalid or expired |
| `WEAK_PASSWORD` | Password does not meet requirements |
| `INTERNAL_ERROR` | Internal server error |
| `SERVICE_UNAVAILABLE` | Service temporarily unavailable |

---

## SDK Usage Examples

See [SDK.md](./SDK.md) for detailed SDK documentation and code examples.

## OpenAPI Specification

The complete OpenAPI 3.0 specification is available at [openapi.yaml](./openapi.yaml).

Interactive API documentation (Swagger UI) is available at:
```
https://api.heimdall.yourdomain.com/docs
```
