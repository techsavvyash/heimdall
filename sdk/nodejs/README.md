# Heimdall Node.js SDK

Official Node.js SDK for [Heimdall Authentication Service](https://github.com/techsavvyash/heimdall).

## Installation

```bash
npm install @heimdall/sdk
```

## Quick Start

```typescript
import { HeimdallClient } from '@heimdall/sdk';

// Initialize the client
const heimdall = new HeimdallClient({
  apiUrl: 'http://localhost:8080',
  tenantId: 'your-tenant-id', // Optional
  autoRefresh: true // Automatically refresh tokens
});

// Register a new user
const user = await heimdall.auth.register({
  email: 'user@example.com',
  password: 'SecurePassword123!',
  firstName: 'John',
  lastName: 'Doe'
});

// Login
const loginResult = await heimdall.auth.login({
  email: 'user@example.com',
  password: 'SecurePassword123!',
  rememberMe: true
});

// Get user profile
const profile = await heimdall.user.getProfile();

// Update profile
await heimdall.user.updateProfile({
  firstName: 'Jane',
  metadata: { theme: 'dark' }
});

// Logout
await heimdall.auth.logout();
```

## Configuration

### Client Options

```typescript
interface HeimdallConfig {
  apiUrl: string;                    // Required: API base URL
  tenantId?: string;                 // Optional: Tenant ID for multi-tenant apps
  storage?: Storage;                 // Optional: Custom storage implementation
  autoRefresh?: boolean;             // Optional: Auto-refresh tokens (default: true)
  refreshBuffer?: number;            // Optional: Minutes before expiry to refresh (default: 5)
  onTokenRefresh?: (tokens: TokenPair) => void;  // Optional: Token refresh callback
  onAuthError?: (error: Error) => void;          // Optional: Auth error callback
  headers?: Record<string, string>;  // Optional: Custom headers
}
```

### Storage

By default, the SDK uses `localStorage` in browsers and in-memory storage in Node.js. You can provide a custom storage implementation:

```typescript
import { Storage } from '@heimdall/sdk';

class CustomStorage implements Storage {
  getItem(key: string): string | null {
    // Your implementation
  }

  setItem(key: string, value: string): void {
    // Your implementation
  }

  removeItem(key: string): void {
    // Your implementation
  }
}

const heimdall = new HeimdallClient({
  apiUrl: 'http://localhost:8080',
  storage: new CustomStorage()
});
```

## API Reference

### Authentication

#### Register

```typescript
await heimdall.auth.register({
  email: 'user@example.com',
  password: 'SecurePassword123!',
  firstName: 'John',
  lastName: 'Doe',
  metadata?: { /* custom data */ }
});
```

#### Login

```typescript
await heimdall.auth.login({
  email: 'user@example.com',
  password: 'SecurePassword123!',
  rememberMe: false // Optional, extends token lifetime
});
```

#### Logout

```typescript
await heimdall.auth.logout();
```

#### Refresh Tokens

```typescript
const tokens = await heimdall.auth.refreshTokens();
```

#### Check Authentication

```typescript
const isAuthenticated = await heimdall.isAuthenticated();
```

### User Management

#### Get Profile

```typescript
const profile = await heimdall.user.getProfile();
```

#### Update Profile

```typescript
await heimdall.user.updateProfile({
  firstName: 'Jane',
  lastName: 'Doe',
  metadata: { theme: 'dark', language: 'en' }
});
```

#### Delete Account

```typescript
await heimdall.user.deleteAccount();
```

## Error Handling

```typescript
import { HeimdallError } from '@heimdall/sdk';

try {
  await heimdall.auth.login({ email, password });
} catch (error) {
  if (error instanceof HeimdallError) {
    console.error('Auth error:', error.message);
    console.error('Status code:', error.statusCode);
    console.error('Details:', error.details);
  }
}
```

## Token Management

The SDK automatically handles token storage and refresh. Tokens are stored using the configured storage mechanism.

### Manual Token Refresh

```typescript
// Tokens are refreshed automatically, but you can also refresh manually
await heimdall.auth.refreshTokens();
```

### Token Refresh Callbacks

```typescript
const heimdall = new HeimdallClient({
  apiUrl: 'http://localhost:8080',
  onTokenRefresh: (tokens) => {
    console.log('Tokens refreshed:', tokens);
  },
  onAuthError: (error) => {
    console.error('Auth error occurred:', error);
    // Redirect to login page, etc.
  }
});
```

## TypeScript Support

The SDK is written in TypeScript and includes full type definitions.

```typescript
import {
  HeimdallClient,
  RegisterRequest,
  LoginRequest,
  UserProfile,
  TokenPair
} from '@heimdall/sdk';
```

## Browser Usage

For browser environments, you can use the standalone version:

```html
<script src="/path/to/heimdall-client.js"></script>
<script>
  const heimdall = new HeimdallClient({
    apiUrl: 'http://localhost:8080'
  });
</script>
```

## Examples

See the [examples directory](../../examples/sample-app) for complete example applications.

## License

MIT

## Support

For issues and questions, please visit [GitHub Issues](https://github.com/techsavvyash/heimdall/issues).
