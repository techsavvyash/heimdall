# Heimdall ElysiaJS Backend Example

This is a sample backend server built with [ElysiaJS](https://elysiajs.com/) (running on [Bun](https://bun.sh/)) that demonstrates how to integrate Heimdall authentication into a modern TypeScript backend.

## Features

- 🚀 **ElysiaJS**: Fast, ergonomic web framework for Bun
- 🔐 **Heimdall Authentication**: Full authentication flow using Heimdall SDK
- 🛡️ **Protected Routes**: Bearer token authentication middleware
- 🌐 **CORS**: Configured for frontend integration
- ⚡ **TypeScript**: Full type safety with Bun's native TypeScript support
- 📦 **Session Management**: In-memory session storage (replace with Redis in production)

## Prerequisites

- [Bun](https://bun.sh/) v1.0.0 or higher
- Heimdall server running on `http://localhost:8080` (or configure `HEIMDALL_API_URL`)

## Installation

1. **Install Bun** (if not already installed):
```bash
curl -fsSL https://bun.sh/install | bash
```

2. **Install dependencies**:
```bash
cd examples/elysia-app
bun install
```

3. **Configure environment**:
```bash
cp .env.example .env
# Edit .env if needed
```

## Running the Server

### Development mode (with auto-reload):
```bash
bun run dev
```

### Production mode:
```bash
bun run start
```

The server will start on `http://localhost:5000` by default.

The frontend is automatically served at `http://localhost:5000` - just open your browser and you're ready to go!

## Demo Frontend

A beautiful HTML demo frontend is included and automatically served by the ElysiaJS server.

### Access the demo:

Simply open http://localhost:5000 in your browser after starting the server.

The demo allows you to:
- Register new users
- Login with existing credentials
- View your profile
- Load protected data
- Logout

No need for a separate frontend server - everything runs on port 5000!

## API Endpoints

### Public Endpoints

#### Health Check
```bash
GET /health
```

Response:
```json
{
  "status": "ok",
  "timestamp": "2025-10-20T10:30:00.000Z"
}
```

#### Root / Demo Frontend
```bash
GET /
```

Returns the demo HTML frontend.

#### API Info
```bash
GET /api
```

Response:
```json
{
  "message": "Heimdall + ElysiaJS Backend API",
  "version": "1.0.0",
  "status": "healthy"
}
```

### Authentication Endpoints

#### Register
```bash
POST /api/auth/register
Content-Type: application/json

{
  "email": "user@example.com",
  "password": "SecurePassword123!",
  "firstName": "John",
  "lastName": "Doe"
}
```

Response:
```json
{
  "success": true,
  "data": {
    "user": {
      "id": "uuid",
      "email": "user@example.com",
      "firstName": "John",
      "lastName": "Doe"
    },
    "accessToken": "eyJhbGci..."
  }
}
```

#### Login
```bash
POST /api/auth/login
Content-Type: application/json

{
  "email": "user@example.com",
  "password": "SecurePassword123!"
}
```

Response:
```json
{
  "success": true,
  "data": {
    "user": {
      "id": "uuid",
      "email": "user@example.com",
      "firstName": "John",
      "lastName": "Doe"
    },
    "accessToken": "eyJhbGci..."
  }
}
```

#### Logout
```bash
POST /api/auth/logout
Authorization: Bearer <your-access-token>
```

Response:
```json
{
  "success": true,
  "message": "Logged out successfully"
}
```

### Protected Endpoints

All protected endpoints require the `Authorization: Bearer <token>` header.

#### Get Current User Profile
```bash
GET /api/users/me
Authorization: Bearer <your-access-token>
```

Response:
```json
{
  "success": true,
  "data": {
    "id": "uuid",
    "email": "user@example.com",
    "firstName": "John",
    "lastName": "Doe"
  }
}
```

#### Update User Profile
```bash
PATCH /api/users/me
Authorization: Bearer <your-access-token>
Content-Type: application/json

{
  "firstName": "Jane",
  "lastName": "Smith"
}
```

#### Get Protected Data (Example)
```bash
GET /api/data
Authorization: Bearer <your-access-token>
```

Response:
```json
{
  "success": true,
  "data": {
    "message": "Hello John!",
    "items": [
      { "id": 1, "name": "Item 1", "description": "Protected data 1" },
      { "id": 2, "name": "Item 2", "description": "Protected data 2" },
      { "id": 3, "name": "Item 3", "description": "Protected data 3" }
    ],
    "timestamp": "2025-10-20T10:30:00.000Z"
  }
}
```

## Quick Test

### Using the Demo Frontend

1. Start the Heimdall server (port 8080)
2. Start the ElysiaJS backend: `bun run dev` (port 5000)
3. Open http://localhost:5000 in your browser
4. Register a new account or login

That's it! The frontend is served directly from the ElysiaJS server.

### Testing with curl

### 1. Register a new user:
```bash
curl -X POST http://localhost:5000/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{
    "email": "test@example.com",
    "password": "SecurePassword123!",
    "firstName": "Test",
    "lastName": "User"
  }'
```

### 2. Login:
```bash
curl -X POST http://localhost:5000/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "test@example.com",
    "password": "SecurePassword123!"
  }'
```

Copy the `accessToken` from the response.

### 3. Get user profile:
```bash
curl http://localhost:5000/api/users/me \
  -H "Authorization: Bearer <YOUR_ACCESS_TOKEN>"
```

### 4. Get protected data:
```bash
curl http://localhost:5000/api/users/me \
  -H "Authorization: Bearer <YOUR_ACCESS_TOKEN>"
```

## Frontend Integration

### Using fetch (if building your own frontend):
```typescript
// Register
const response = await fetch('/api/auth/register', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({
    email: 'user@example.com',
    password: 'SecurePassword123!',
    firstName: 'John',
    lastName: 'Doe'
  })
});

const { data } = await response.json();
const accessToken = data.accessToken;

// Use token for authenticated requests
const profileResponse = await fetch('/api/users/me', {
  headers: { 'Authorization': `Bearer ${accessToken}` }
});

const profile = await profileResponse.json();
```

### Using axios (if building your own frontend):
```typescript
import axios from 'axios';

const api = axios.create({
  baseURL: '/api'
});

// Login
const { data } = await api.post('/auth/login', {
  email: 'user@example.com',
  password: 'SecurePassword123!'
});

// Set token for future requests
api.defaults.headers.common['Authorization'] = `Bearer ${data.data.accessToken}`;

// Get profile
const profile = await api.get('/users/me');
```

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│              ElysiaJS Server (Bun)                              │
│              http://localhost:5000                               │
│                                                                  │
│  ┌──────────────────────┐    ┌──────────────────────────────┐  │
│  │  Static Frontend     │    │      API Routes              │  │
│  │  (HTML/CSS/JS)       │    │  ┌────────┐  ┌────────────┐ │  │
│  │                      │    │  │ Auth   │  │ Protected  │ │  │
│  │  GET /               │    │  │ /api/* │  │  Routes    │ │  │
│  │  (Demo UI)           │    │  └───┬────┘  └─────┬──────┘ │  │
│  └──────────────────────┘    │      │             │        │  │
│                               │      │  Heimdall SDK       │  │
│                               │      └──────────┬──────────┘  │
│                               └─────────────────┼─────────────┘
└─────────────────────────────────────────────────┼─────────────┘
                                                  │ HTTP/REST
                                                  │
                              ┌───────────────────▼────────────────┐
                              │      Heimdall Server (Go)          │
                              │      http://localhost:8080         │
                              └────────────────────────────────────┘
```

## Session Management

**Current Implementation**: In-memory Map (development only)

**Production Recommendation**: Use Redis for session storage:

```typescript
import { Redis } from "ioredis";

const redis = new Redis(process.env.REDIS_URL);

// Store session
await redis.set(`session:${token}`, JSON.stringify(user), 'EX', 3600);

// Get session
const userData = await redis.get(`session:${token}`);
const user = userData ? JSON.parse(userData) : null;

// Delete session
await redis.del(`session:${token}`);
```

## Deployment

### Docker
```dockerfile
FROM oven/bun:latest

WORKDIR /app

COPY package.json bun.lockb ./
RUN bun install --frozen-lockfile

COPY . .

EXPOSE 5000

CMD ["bun", "src/index.ts"]
```

Build and run:
```bash
docker build -t heimdall-elysia-backend .
docker run -p 5000:5000 --env-file .env heimdall-elysia-backend
```

## Why ElysiaJS + Bun?

- **Performance**: Bun is 3x faster than Node.js
- **Native TypeScript**: No transpilation needed
- **Modern API**: Clean, intuitive API design
- **Small footprint**: Minimal dependencies
- **Built-in features**: JWT, CORS, validation out of the box

## Security Considerations

1. **Environment Variables**: Never commit `.env` file
2. **CORS**: Configure `FRONTEND_URL` to match your frontend domain
3. **HTTPS**: Use HTTPS in production
4. **Token Storage**: Use httpOnly cookies or secure storage
5. **Session Store**: Replace in-memory storage with Redis in production
6. **Rate Limiting**: Add rate limiting for auth endpoints
7. **Input Validation**: Add request body validation

## Troubleshooting

### Bun not installed
```bash
curl -fsSL https://bun.sh/install | bash
```

### Connection refused to Heimdall
- Ensure Heimdall server is running: `curl http://localhost:8080/health`
- Check `HEIMDALL_API_URL` in `.env`

### CORS errors (if using external frontend)
- Update `FRONTEND_URL` in `.env` to match your external frontend URL
- For the built-in demo frontend, no CORS configuration needed

## License

MIT
