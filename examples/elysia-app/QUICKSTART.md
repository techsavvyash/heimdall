# ElysiaJS + Heimdall - Quick Start Guide

Get up and running with Heimdall authentication in under 5 minutes!

## Prerequisites

- [Bun](https://bun.sh/) v1.0.0 or higher
- Heimdall server running on port 8080

## Step-by-Step Setup

### 1. Install Bun (if not already installed)

```bash
curl -fsSL https://bun.sh/install | bash
```

### 2. Navigate to the ElysiaJS Example

```bash
cd examples/elysia-app
```

### 3. Install Dependencies

```bash
bun install
```

This installs:
- `elysia` - Web framework
- `@elysiajs/cors` - CORS support
- `@elysiajs/bearer` - Bearer token authentication
- `@elysiajs/static` - Static file serving
- `@techsavvyash/heimdall-sdk` - Heimdall authentication SDK

### 4. Configure Environment (Optional)

The defaults work out of the box, but you can customize:

```bash
cp .env.example .env
# Edit .env if needed
```

Default configuration:
- Server runs on port **5000**
- Heimdall API at **http://localhost:8080**
- Frontend served at **http://localhost:5000**

### 5. Start the Server

```bash
bun run dev
```

You should see:
```
🦊 ElysiaJS + Heimdall server is running at http://localhost:5000
```

### 6. Access the Demo

Open your browser and navigate to:

**http://localhost:5000**

That's it! 🎉

## What You Get

### Frontend (Served at `/`)
- Beautiful, responsive UI
- Login/Register forms
- User profile display
- Protected data loading
- Session management

### API Endpoints (at `/api/*`)
- `POST /api/auth/register` - Register new user
- `POST /api/auth/login` - Login user
- `POST /api/auth/logout` - Logout (requires token)
- `GET /api/users/me` - Get user profile (requires token)
- `PATCH /api/users/me` - Update profile (requires token)
- `GET /api/data` - Get protected data (requires token)

### Health Checks
- `GET /health` - Server health
- `GET /api` - API info

## Quick Test Flow

1. **Start Heimdall server** (in another terminal):
   ```bash
   # From the project root
   go run cmd/server/main.go
   ```

2. **Start ElysiaJS server**:
   ```bash
   bun run dev
   ```

3. **Open browser** to http://localhost:5000

4. **Register** a new account:
   - Email: `test@example.com`
   - Password: `SecurePassword123!`
   - First Name: `Test`
   - Last Name: `User`

5. **Login** with your credentials

6. **View your profile** automatically loaded

7. **Load protected data** by clicking the button

8. **Logout** when done

## Testing with curl

### Register:
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

### Login:
```bash
curl -X POST http://localhost:5000/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "test@example.com",
    "password": "SecurePassword123!"
  }'
```

Copy the `accessToken` from the response.

### Get Profile:
```bash
curl http://localhost:5000/api/users/me \
  -H "Authorization: Bearer YOUR_ACCESS_TOKEN_HERE"
```

## Architecture

Everything runs on **one server** (port 5000):

```
┌─────────────────────────────────────────┐
│    Browser (http://localhost:5000)     │
└───────────┬─────────────────────────────┘
            │
            │ Frontend (GET /)
            │ API calls (POST /api/*)
            │
┌───────────▼─────────────────────────────┐
│      ElysiaJS Server (Bun)              │
│      - Serves static HTML/CSS/JS        │
│      - Handles API requests             │
│      - Uses Heimdall SDK                │
└───────────┬─────────────────────────────┘
            │
            │ HTTP REST calls
            │
┌───────────▼─────────────────────────────┐
│    Heimdall Server (Go:8080)            │
│    - Authentication & Authorization     │
│    - User management                    │
│    - Token management                   │
└─────────────────────────────────────────┘
```

## Development Mode

For development with auto-reload:

```bash
bun run dev
```

The server will automatically restart when you make changes to:
- `src/index.ts`
- Any other source files

## Production Mode

For production deployment:

```bash
bun run start
```

## Troubleshooting

### Port 5000 already in use
```bash
# Find what's using port 5000
lsof -i :5000

# Kill the process
kill -9 <PID>

# Or use a different port
PORT=5001 bun run dev
```

### Cannot connect to Heimdall
```bash
# Check if Heimdall is running
curl http://localhost:8080/health

# If not, start Heimdall
go run cmd/server/main.go
```

### Bun not found
```bash
# Install Bun
curl -fsSL https://bun.sh/install | bash

# Restart terminal or source
source ~/.bashrc  # or ~/.zshrc
```

### 401 Unauthorized errors
- Check that your access token is valid
- Token might be expired (default: 15 minutes)
- Try logging in again to get a fresh token

## Next Steps

- **Build your frontend**: Use React, Vue, Svelte, etc., and connect to `/api/*`
- **Add more endpoints**: Extend `src/index.ts` with your business logic
- **Deploy to production**: See main README for deployment guides
- **Use Redis**: Replace in-memory sessions with Redis for production

## Resources

- [ElysiaJS Documentation](https://elysiajs.com/)
- [Bun Documentation](https://bun.sh/docs)
- [Heimdall Documentation](../../README.md)
- [Full API Reference](./README.md#api-endpoints)

## Support

- GitHub Issues: [https://github.com/techsavvyash/heimdall/issues](https://github.com/techsavvyash/heimdall/issues)
- Main Documentation: [../../README.md](../../README.md)

---

**Happy coding!** 🚀
