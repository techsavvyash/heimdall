# Heimdall SDK Documentation

## Overview

Heimdall provides official SDKs for JavaScript/TypeScript and Go, making it easy to integrate authentication into your applications. Both SDKs offer a consistent API and handle token management, automatic refresh, and error handling.

## Table of Contents

- [JavaScript/TypeScript SDK](#javascripttypescript-sdk)
  - [Installation](#installation)
  - [Configuration](#configuration)
  - [Usage](#usage)
  - [API Reference](#api-reference-jsts)
  - [Framework Integration](#framework-integration)
- [Go SDK](#go-sdk)
  - [Installation](#installation-1)
  - [Configuration](#configuration-1)
  - [Usage](#usage-1)
  - [API Reference](#api-reference-go)
  - [Middleware](#middleware)
- [Common Patterns](#common-patterns)

---

## JavaScript/TypeScript SDK

### Installation

```bash
# npm
npm install @heimdall/sdk

# yarn
yarn add @heimdall/sdk

# pnpm
pnpm add @heimdall/sdk
```

### Configuration

#### Basic Configuration

```typescript
import { HeimdallClient } from '@heimdall/sdk';

const heimdall = new HeimdallClient({
  apiUrl: 'https://api.heimdall.yourdomain.com',
  tenantId: 'your-tenant-id', // Optional if using subdomain
  // Optional: custom storage for tokens
  storage: localStorage, // Default: localStorage in browser, memory in Node.js
  // Optional: auto refresh tokens
  autoRefresh: true, // Default: true
  // Optional: custom headers
  headers: {
    'X-Custom-Header': 'value'
  }
});
```

#### Advanced Configuration

```typescript
const heimdall = new HeimdallClient({
  apiUrl: 'https://api.heimdall.yourdomain.com',
  tenantId: 'your-tenant-id',
  storage: customStorage, // Custom storage implementation
  autoRefresh: true,
  refreshBuffer: 60, // Refresh token 60 seconds before expiry
  onTokenRefresh: (tokens) => {
    console.log('Tokens refreshed:', tokens);
  },
  onAuthError: (error) => {
    console.error('Authentication error:', error);
    // Redirect to login page
  },
  interceptors: {
    request: (config) => {
      // Modify request before sending
      return config;
    },
    response: (response) => {
      // Handle response
      return response;
    },
    error: (error) => {
      // Handle errors
      throw error;
    }
  }
});
```

### Usage

#### Authentication

##### Register

```typescript
// Register a new user
try {
  const { user } = await heimdall.auth.register({
    email: 'user@example.com',
    password: 'SecurePassword123!',
    firstName: 'John',
    lastName: 'Doe',
    metadata: {
      source: 'web'
    }
  });

  console.log('User registered:', user);
  // Check email for verification link
} catch (error) {
  if (error.code === 'CONFLICT') {
    console.error('Email already exists');
  }
}
```

##### Login

```typescript
// Login with email and password
try {
  const { user, accessToken, refreshToken } = await heimdall.auth.login({
    email: 'user@example.com',
    password: 'SecurePassword123!',
    rememberMe: true
  });

  console.log('Logged in:', user);
  // Tokens are automatically stored

} catch (error) {
  if (error.code === 'UNAUTHORIZED') {
    console.error('Invalid credentials');
  } else if (error.code === 'ACCOUNT_LOCKED') {
    console.error('Account locked due to too many failed attempts');
  }
}

// Check if MFA is required
const response = await heimdall.auth.login({ ... });
if (response.mfaRequired) {
  // Show MFA verification UI
  const { mfaToken, methods } = response;
  // Use mfaToken to verify MFA
}
```

##### OAuth Login

```typescript
// Initiate OAuth flow
heimdall.auth.loginWithOAuth('google', {
  redirectUri: 'https://yourapp.com/auth/callback',
  scope: 'openid profile email'
});

// In your callback page
try {
  const { user, tokens } = await heimdall.auth.handleOAuthCallback();
  console.log('OAuth login successful:', user);
} catch (error) {
  console.error('OAuth login failed:', error);
}
```

##### Passwordless (Magic Link)

```typescript
// Request magic link
await heimdall.auth.requestMagicLink({
  email: 'user@example.com',
  redirectUri: 'https://yourapp.com/auth/verify'
});

// User clicks link in email, in your verify page:
try {
  const params = new URLSearchParams(window.location.search);
  const token = params.get('token');

  const { user, tokens } = await heimdall.auth.verifyMagicLink(token);
  console.log('Logged in via magic link:', user);
} catch (error) {
  console.error('Invalid or expired magic link');
}
```

##### MFA (TOTP)

```typescript
// Enroll in TOTP
const { secret, qrCode, recoveryCodes } = await heimdall.auth.mfa.enrollTOTP();

// Display QR code to user
document.getElementById('qr-code').src = qrCode;

// Store recovery codes securely
console.log('Recovery codes:', recoveryCodes);

// Verify TOTP code to complete enrollment
await heimdall.auth.mfa.verifyTOTP({
  code: '123456'
});

// During login with MFA
const loginResponse = await heimdall.auth.login({ ... });
if (loginResponse.mfaRequired) {
  const { user, tokens } = await heimdall.auth.mfa.verifyTOTP({
    code: '123456',
    mfaToken: loginResponse.mfaToken
  });
}
```

##### Logout

```typescript
// Logout current user
await heimdall.auth.logout();
console.log('Logged out');
```

##### Token Management

```typescript
// Check if user is authenticated
const isAuthenticated = heimdall.auth.isAuthenticated();

// Get current access token
const accessToken = heimdall.auth.getAccessToken();

// Get current user from token
const user = heimdall.auth.getCurrentUser();

// Manually refresh token
const { accessToken, refreshToken } = await heimdall.auth.refreshToken();

// Listen for authentication state changes
heimdall.auth.onAuthStateChanged((user) => {
  if (user) {
    console.log('User logged in:', user);
  } else {
    console.log('User logged out');
  }
});
```

#### User Management

```typescript
// Get current user profile
const user = await heimdall.users.me();
console.log('Current user:', user);

// Update current user profile
const updatedUser = await heimdall.users.updateMe({
  firstName: 'Jane',
  phoneNumber: '+1234567890',
  metadata: {
    preferences: {
      theme: 'dark'
    }
  }
});

// Change password
await heimdall.users.changePassword({
  currentPassword: 'OldPassword123!',
  newPassword: 'NewPassword123!'
});

// Request password reset
await heimdall.users.requestPasswordReset({
  email: 'user@example.com'
});

// Reset password with token
await heimdall.users.resetPassword({
  token: 'reset_token_from_email',
  newPassword: 'NewPassword123!'
});
```

#### User Management (Admin)

```typescript
// List users (admin only)
const { users, pagination } = await heimdall.users.list({
  page: 1,
  limit: 20,
  search: 'john',
  role: 'admin',
  status: 'active'
});

// Get user by ID
const user = await heimdall.users.getById('user-id');

// Create user
const newUser = await heimdall.users.create({
  email: 'newuser@example.com',
  password: 'SecurePassword123!',
  firstName: 'Jane',
  lastName: 'Doe',
  emailVerified: true,
  roles: ['user'],
  sendVerificationEmail: false
});

// Update user
const updatedUser = await heimdall.users.update('user-id', {
  firstName: 'Jane',
  status: 'suspended'
});

// Delete user
await heimdall.users.delete('user-id', { hardDelete: false });

// Bulk import users
const result = await heimdall.users.import({
  users: [
    { email: 'user1@example.com', firstName: 'User', lastName: 'One' },
    { email: 'user2@example.com', firstName: 'User', lastName: 'Two' }
  ],
  sendVerificationEmails: true
});
console.log(`Imported ${result.imported} users`);
```

#### RBAC

```typescript
// List roles
const roles = await heimdall.rbac.listRoles();

// Create role
const role = await heimdall.rbac.createRole({
  name: 'editor',
  description: 'Content editor role',
  permissions: ['read:posts', 'write:posts', 'delete:own_posts']
});

// Update role
await heimdall.rbac.updateRole('role-id', {
  permissions: ['read:posts', 'write:posts', 'delete:posts']
});

// Delete role
await heimdall.rbac.deleteRole('role-id');

// Assign role to user
await heimdall.rbac.assignRole('user-id', ['role-id']);

// Remove role from user
await heimdall.rbac.removeRole('user-id', 'role-id');

// Check permission
const hasPermission = await heimdall.rbac.checkPermission({
  userId: 'user-id',
  permission: 'write:posts',
  resource: 'post-123'
});
```

#### Audit Logs

```typescript
// Query audit logs (admin only)
const { logs, pagination } = await heimdall.audit.query({
  page: 1,
  limit: 50,
  userId: 'user-id',
  eventType: 'auth.login',
  startDate: new Date('2024-01-01'),
  endDate: new Date('2024-01-31')
});

// Export audit logs
const csvData = await heimdall.audit.export({
  format: 'csv',
  startDate: new Date('2024-01-01'),
  endDate: new Date('2024-01-31')
});
```

### API Reference (JS/TS)

#### HeimdallClient

```typescript
class HeimdallClient {
  constructor(config: HeimdallConfig);

  // Namespaces
  auth: AuthService;
  users: UserService;
  rbac: RBACService;
  audit: AuditService;
  tenants: TenantService;

  // Methods
  setAccessToken(token: string): void;
  getAccessToken(): string | null;
  clearTokens(): void;
}
```

#### AuthService

```typescript
interface AuthService {
  // Registration & Login
  register(data: RegisterData): Promise<RegisterResponse>;
  login(data: LoginData): Promise<LoginResponse>;
  logout(): Promise<void>;

  // OAuth
  loginWithOAuth(provider: string, options?: OAuthOptions): void;
  handleOAuthCallback(): Promise<OAuthCallbackResponse>;

  // Passwordless
  requestMagicLink(data: MagicLinkRequest): Promise<void>;
  verifyMagicLink(token: string): Promise<LoginResponse>;

  // MFA
  mfa: {
    enrollTOTP(): Promise<TOTPEnrollResponse>;
    verifyTOTP(data: TOTPVerifyRequest): Promise<LoginResponse>;
  };

  // Token Management
  refreshToken(): Promise<TokenResponse>;
  isAuthenticated(): boolean;
  getCurrentUser(): User | null;
  onAuthStateChanged(callback: (user: User | null) => void): () => void;

  // Token Accessors
  getAccessToken(): string | null;
  getRefreshToken(): string | null;
  getIdToken(): string | null;
}
```

#### UserService

```typescript
interface UserService {
  // Current User
  me(): Promise<UserDetail>;
  updateMe(data: UpdateUserData): Promise<UserDetail>;
  changePassword(data: PasswordChangeData): Promise<void>;

  // Password Management
  requestPasswordReset(data: { email: string }): Promise<void>;
  resetPassword(data: PasswordResetData): Promise<void>;

  // Admin Operations
  list(params?: ListUsersParams): Promise<UserListResponse>;
  getById(userId: string): Promise<UserDetail>;
  create(data: CreateUserData): Promise<UserDetail>;
  update(userId: string, data: UpdateUserData): Promise<UserDetail>;
  delete(userId: string, options?: DeleteOptions): Promise<void>;
  import(data: ImportUsersData): Promise<ImportResult>;
}
```

### Framework Integration

#### React

```typescript
// HeimdallProvider.tsx
import { createContext, useContext, useEffect, useState } from 'react';
import { HeimdallClient, User } from '@heimdall/sdk';

const HeimdallContext = createContext<{
  heimdall: HeimdallClient;
  user: User | null;
  loading: boolean;
} | null>(null);

export function HeimdallProvider({ children, config }) {
  const [heimdall] = useState(() => new HeimdallClient(config));
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const unsubscribe = heimdall.auth.onAuthStateChanged((user) => {
      setUser(user);
      setLoading(false);
    });

    return unsubscribe;
  }, [heimdall]);

  return (
    <HeimdallContext.Provider value={{ heimdall, user, loading }}>
      {children}
    </HeimdallContext.Provider>
  );
}

export function useHeimdall() {
  const context = useContext(HeimdallContext);
  if (!context) {
    throw new Error('useHeimdall must be used within HeimdallProvider');
  }
  return context;
}

// Usage in components
function LoginPage() {
  const { heimdall } = useHeimdall();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');

  const handleLogin = async (e) => {
    e.preventDefault();
    try {
      await heimdall.auth.login({ email, password });
      // User state will be updated automatically
    } catch (error) {
      console.error('Login failed:', error);
    }
  };

  return (
    <form onSubmit={handleLogin}>
      <input type="email" value={email} onChange={e => setEmail(e.target.value)} />
      <input type="password" value={password} onChange={e => setPassword(e.target.value)} />
      <button type="submit">Login</button>
    </form>
  );
}

// Protected Route
function ProtectedRoute({ children }) {
  const { user, loading } = useHeimdall();

  if (loading) return <div>Loading...</div>;
  if (!user) return <Navigate to="/login" />;

  return children;
}
```

#### Vue

```typescript
// heimdall.ts
import { HeimdallClient } from '@heimdall/sdk';
import { reactive, readonly } from 'vue';

const state = reactive({
  user: null,
  loading: true
});

export const heimdall = new HeimdallClient({
  apiUrl: 'https://api.heimdall.yourdomain.com',
  tenantId: 'your-tenant-id'
});

heimdall.auth.onAuthStateChanged((user) => {
  state.user = user;
  state.loading = false;
});

export function useHeimdall() {
  return {
    heimdall,
    state: readonly(state)
  };
}

// Usage in components
<script setup>
import { useHeimdall } from './heimdall';

const { heimdall, state } = useHeimdall();

const login = async (email, password) => {
  await heimdall.auth.login({ email, password });
};
</script>
```

#### Node.js/Express

```typescript
import express from 'express';
import { HeimdallClient } from '@heimdall/sdk';

const app = express();

const heimdall = new HeimdallClient({
  apiUrl: 'https://api.heimdall.yourdomain.com',
  tenantId: 'your-tenant-id'
});

// Authentication middleware
function requireAuth(req, res, next) {
  const token = req.headers.authorization?.replace('Bearer ', '');

  if (!token) {
    return res.status(401).json({ error: 'Unauthorized' });
  }

  try {
    heimdall.setAccessToken(token);
    const user = heimdall.auth.getCurrentUser();

    if (!user) {
      return res.status(401).json({ error: 'Invalid token' });
    }

    req.user = user;
    next();
  } catch (error) {
    res.status(401).json({ error: 'Invalid token' });
  }
}

// Protected route
app.get('/api/profile', requireAuth, async (req, res) => {
  const user = await heimdall.users.me();
  res.json(user);
});
```

---

## Go SDK

### Installation

```bash
go get github.com/techsavvyash/heimdall-go
```

### Configuration

#### Basic Configuration

```go
package main

import (
    "context"
    heimdall "github.com/techsavvyash/heimdall-go"
)

func main() {
    client, err := heimdall.NewClient(&heimdall.Config{
        APIUrl:   "https://api.heimdall.yourdomain.com",
        TenantID: "your-tenant-id",
    })
    if err != nil {
        panic(err)
    }

    defer client.Close()
}
```

#### Advanced Configuration

```go
client, err := heimdall.NewClient(&heimdall.Config{
    APIUrl:   "https://api.heimdall.yourdomain.com",
    TenantID: "your-tenant-id",

    // Optional: Custom HTTP client
    HTTPClient: &http.Client{
        Timeout: 30 * time.Second,
    },

    // Optional: Auto refresh tokens
    AutoRefresh: true,
    RefreshBuffer: 60 * time.Second,

    // Optional: Custom headers
    Headers: map[string]string{
        "X-Custom-Header": "value",
    },

    // Optional: Interceptors
    RequestInterceptor: func(req *http.Request) error {
        // Modify request before sending
        return nil
    },
    ResponseInterceptor: func(resp *http.Response) error {
        // Handle response
        return nil
    },
})
```

### Usage

#### Authentication

##### Register

```go
ctx := context.Background()

user, err := client.Auth.Register(ctx, &heimdall.RegisterRequest{
    Email:     "user@example.com",
    Password:  "SecurePassword123!",
    FirstName: "John",
    LastName:  "Doe",
    Metadata: map[string]interface{}{
        "source": "web",
    },
})
if err != nil {
    if heimdall.IsConflictError(err) {
        log.Println("Email already exists")
    }
    panic(err)
}

log.Printf("User registered: %+v\n", user)
```

##### Login

```go
ctx := context.Background()

resp, err := client.Auth.Login(ctx, &heimdall.LoginRequest{
    Email:      "user@example.com",
    Password:   "SecurePassword123!",
    RememberMe: true,
})
if err != nil {
    if heimdall.IsUnauthorizedError(err) {
        log.Println("Invalid credentials")
    }
    panic(err)
}

// Check if MFA is required
if resp.MFARequired {
    log.Println("MFA required")
    // Handle MFA verification
} else {
    log.Printf("Logged in: %+v\n", resp.User)
    // Tokens are available in resp.AccessToken, resp.RefreshToken
}
```

##### OAuth Login

```go
// Get OAuth authorization URL
url, state, err := client.Auth.GetOAuthURL("google", &heimdall.OAuthOptions{
    RedirectURI: "https://yourapp.com/auth/callback",
    Scope:       "openid profile email",
})
if err != nil {
    panic(err)
}

// Redirect user to URL
// After callback, exchange code for tokens
resp, err := client.Auth.HandleOAuthCallback(ctx, code, state)
if err != nil {
    panic(err)
}

log.Printf("OAuth login successful: %+v\n", resp.User)
```

##### Passwordless (Magic Link)

```go
// Request magic link
err := client.Auth.RequestMagicLink(ctx, &heimdall.MagicLinkRequest{
    Email:       "user@example.com",
    RedirectURI: "https://yourapp.com/auth/verify",
})
if err != nil {
    panic(err)
}

// Verify magic link token
resp, err := client.Auth.VerifyMagicLink(ctx, token)
if err != nil {
    panic(err)
}

log.Printf("Logged in via magic link: %+v\n", resp.User)
```

##### MFA (TOTP)

```go
// Enroll in TOTP
enrollResp, err := client.Auth.MFA.EnrollTOTP(ctx)
if err != nil {
    panic(err)
}

log.Printf("Secret: %s\n", enrollResp.Secret)
log.Printf("QR Code: %s\n", enrollResp.QRCode)
log.Printf("Recovery Codes: %v\n", enrollResp.RecoveryCodes)

// Verify TOTP to complete enrollment
resp, err := client.Auth.MFA.VerifyTOTP(ctx, &heimdall.TOTPVerifyRequest{
    Code: "123456",
})
if err != nil {
    panic(err)
}
```

##### Token Management

```go
// Set access token
client.SetAccessToken("eyJhbGci...")

// Get current access token
token := client.GetAccessToken()

// Get current user from token
user, err := client.Auth.GetCurrentUser()
if err != nil {
    panic(err)
}

// Refresh token
tokens, err := client.Auth.RefreshToken(ctx)
if err != nil {
    panic(err)
}

log.Printf("New access token: %s\n", tokens.AccessToken)
```

#### User Management

```go
// Get current user
user, err := client.Users.Me(ctx)
if err != nil {
    panic(err)
}

// Update current user
updatedUser, err := client.Users.UpdateMe(ctx, &heimdall.UpdateUserRequest{
    FirstName: heimdall.String("Jane"),
    Metadata: map[string]interface{}{
        "preferences": map[string]string{
            "theme": "dark",
        },
    },
})

// Change password
err = client.Users.ChangePassword(ctx, &heimdall.PasswordChangeRequest{
    CurrentPassword: "OldPassword123!",
    NewPassword:     "NewPassword123!",
})
```

#### User Management (Admin)

```go
// List users
users, pagination, err := client.Users.List(ctx, &heimdall.ListUsersParams{
    Page:   1,
    Limit:  20,
    Search: "john",
    Role:   "admin",
    Status: "active",
})
if err != nil {
    panic(err)
}

// Create user
newUser, err := client.Users.Create(ctx, &heimdall.CreateUserRequest{
    Email:         "newuser@example.com",
    Password:      "SecurePassword123!",
    FirstName:     "Jane",
    LastName:      "Doe",
    EmailVerified: true,
    Roles:         []string{"user"},
})

// Update user
updatedUser, err := client.Users.Update(ctx, "user-id", &heimdall.UpdateUserRequest{
    FirstName: heimdall.String("Jane"),
    Status:    heimdall.String("suspended"),
})

// Delete user
err = client.Users.Delete(ctx, "user-id", &heimdall.DeleteOptions{
    HardDelete: false,
})
```

#### RBAC

```go
// List roles
roles, err := client.RBAC.ListRoles(ctx)
if err != nil {
    panic(err)
}

// Create role
role, err := client.RBAC.CreateRole(ctx, &heimdall.CreateRoleRequest{
    Name:        "editor",
    Description: "Content editor role",
    Permissions: []string{"read:posts", "write:posts"},
})

// Assign role to user
err = client.RBAC.AssignRole(ctx, "user-id", []string{"role-id"})

// Check permission
hasPermission, err := client.RBAC.CheckPermission(ctx, &heimdall.PermissionCheckRequest{
    UserID:     "user-id",
    Permission: "write:posts",
    Resource:   "post-123",
})
```

### API Reference (Go)

#### Client

```go
type Client struct {
    Auth    *AuthService
    Users   *UserService
    RBAC    *RBACService
    Audit   *AuditService
    Tenants *TenantService
}

func NewClient(config *Config) (*Client, error)
func (c *Client) SetAccessToken(token string)
func (c *Client) GetAccessToken() string
func (c *Client) Close() error
```

#### AuthService

```go
type AuthService struct {}

// Registration & Login
func (s *AuthService) Register(ctx context.Context, req *RegisterRequest) (*User, error)
func (s *AuthService) Login(ctx context.Context, req *LoginRequest) (*LoginResponse, error)
func (s *AuthService) Logout(ctx context.Context) error

// OAuth
func (s *AuthService) GetOAuthURL(provider string, opts *OAuthOptions) (url, state string, err error)
func (s *AuthService) HandleOAuthCallback(ctx context.Context, code, state string) (*LoginResponse, error)

// Passwordless
func (s *AuthService) RequestMagicLink(ctx context.Context, req *MagicLinkRequest) error
func (s *AuthService) VerifyMagicLink(ctx context.Context, token string) (*LoginResponse, error)

// MFA
type MFAService struct {}
func (s *MFAService) EnrollTOTP(ctx context.Context) (*TOTPEnrollResponse, error)
func (s *MFAService) VerifyTOTP(ctx context.Context, req *TOTPVerifyRequest) (*LoginResponse, error)

// Token Management
func (s *AuthService) RefreshToken(ctx context.Context) (*TokenResponse, error)
func (s *AuthService) GetCurrentUser() (*User, error)
```

### Middleware

#### HTTP Middleware

```go
package main

import (
    "net/http"
    heimdall "github.com/techsavvyash/heimdall-go"
)

func AuthMiddleware(client *heimdall.Client) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            token := r.Header.Get("Authorization")
            if token == "" {
                http.Error(w, "Unauthorized", http.StatusUnauthorized)
                return
            }

            token = strings.TrimPrefix(token, "Bearer ")
            client.SetAccessToken(token)

            user, err := client.Auth.GetCurrentUser()
            if err != nil {
                http.Error(w, "Invalid token", http.StatusUnauthorized)
                return
            }

            // Add user to context
            ctx := context.WithValue(r.Context(), "user", user)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

// Usage
http.Handle("/api/profile", AuthMiddleware(client)(profileHandler))
```

#### Gin Middleware

```go
import (
    "github.com/gin-gonic/gin"
    heimdall "github.com/techsavvyash/heimdall-go"
)

func HeimdallAuthMiddleware(client *heimdall.Client) gin.HandlerFunc {
    return func(c *gin.Context) {
        token := c.GetHeader("Authorization")
        if token == "" {
            c.JSON(401, gin.H{"error": "Unauthorized"})
            c.Abort()
            return
        }

        token = strings.TrimPrefix(token, "Bearer ")
        client.SetAccessToken(token)

        user, err := client.Auth.GetCurrentUser()
        if err != nil {
            c.JSON(401, gin.H{"error": "Invalid token"})
            c.Abort()
            return
        }

        c.Set("user", user)
        c.Next()
    }
}

// Usage
router := gin.Default()
router.Use(HeimdallAuthMiddleware(client))
router.GET("/api/profile", profileHandler)
```

---

## Common Patterns

### Error Handling

#### JavaScript/TypeScript

```typescript
import { HeimdallError } from '@heimdall/sdk';

try {
  await heimdall.auth.login({ email, password });
} catch (error) {
  if (error instanceof HeimdallError) {
    switch (error.code) {
      case 'UNAUTHORIZED':
        console.error('Invalid credentials');
        break;
      case 'ACCOUNT_LOCKED':
        console.error('Account locked');
        break;
      case 'RATE_LIMIT_EXCEEDED':
        console.error('Too many attempts');
        break;
      default:
        console.error('Error:', error.message);
    }
  } else {
    console.error('Unexpected error:', error);
  }
}
```

#### Go

```go
import heimdall "github.com/techsavvyash/heimdall-go"

_, err := client.Auth.Login(ctx, req)
if err != nil {
    switch {
    case heimdall.IsUnauthorizedError(err):
        log.Println("Invalid credentials")
    case heimdall.IsAccountLockedError(err):
        log.Println("Account locked")
    case heimdall.IsRateLimitError(err):
        log.Println("Too many attempts")
    default:
        log.Printf("Error: %v\n", err)
    }
}
```

### Token Refresh

Both SDKs handle token refresh automatically when `autoRefresh` is enabled. However, you can also manually refresh tokens:

#### JavaScript/TypeScript

```typescript
// Automatic refresh is enabled by default
const heimdall = new HeimdallClient({
  apiUrl: 'https://api.heimdall.yourdomain.com',
  autoRefresh: true,
  refreshBuffer: 60 // Refresh 60 seconds before expiry
});

// Manual refresh
const { accessToken, refreshToken } = await heimdall.auth.refreshToken();
```

#### Go

```go
client, _ := heimdall.NewClient(&heimdall.Config{
    APIUrl:        "https://api.heimdall.yourdomain.com",
    AutoRefresh:   true,
    RefreshBuffer: 60 * time.Second,
})

// Manual refresh
tokens, err := client.Auth.RefreshToken(ctx)
```

### Permission Checking

#### JavaScript/TypeScript

```typescript
// Check if current user has permission
const canWrite = await heimdall.rbac.checkPermission({
  permission: 'write:posts',
  resource: 'post-123'
});

if (canWrite) {
  // Allow write operation
}
```

#### Go

```go
hasPermission, err := client.RBAC.CheckPermission(ctx, &heimdall.PermissionCheckRequest{
    Permission: "write:posts",
    Resource:   "post-123",
})

if err != nil {
    return err
}

if hasPermission {
    // Allow write operation
}
```

## Support

For issues, feature requests, or questions:
- GitHub: https://github.com/techsavvyash/heimdall
- Documentation: https://docs.heimdall.yourdomain.com
- Email: support@yourdomain.com
