# Railway Deployment Guide for Heimdall

This guide will walk you through deploying Heimdall on [Railway](https://railway.app/), a modern platform for deploying applications with ease.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Architecture Overview](#architecture-overview)
- [Deployment Steps](#deployment-steps)
  - [1. Create a New Project](#1-create-a-new-project)
  - [2. Deploy PostgreSQL](#2-deploy-postgresql)
  - [3. Deploy Redis](#3-deploy-redis)
  - [4. Deploy FusionAuth](#4-deploy-fusionauth)
  - [5. Deploy Heimdall](#5-deploy-heimdall)
- [Environment Variables](#environment-variables)
- [Post-Deployment Configuration](#post-deployment-configuration)
- [Monitoring and Logs](#monitoring-and-logs)
- [Troubleshooting](#troubleshooting)
- [Cost Estimation](#cost-estimation)

---

## Prerequisites

1. A [Railway account](https://railway.app/) (free tier available)
2. Railway CLI installed (optional, but recommended):
   ```bash
   npm install -g @railway/cli
   # or
   brew install railway
   ```
3. Git repository with your Heimdall code

---

## Architecture Overview

Heimdall deployment on Railway consists of 4 services:

```
┌─────────────────────────────────────────────────────────┐
│                    Railway Project                       │
│                                                          │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  │
│  │  PostgreSQL  │  │    Redis     │  │  FusionAuth  │  │
│  │   Database   │  │    Cache     │  │  Auth Engine │  │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘  │
│         │                 │                  │          │
│         └─────────────────┼──────────────────┘          │
│                           │                             │
│                   ┌───────▼────────┐                    │
│                   │   Heimdall     │                    │
│                   │   API Gateway  │                    │
│                   └───────┬────────┘                    │
│                           │                             │
└───────────────────────────┼─────────────────────────────┘
                            │
                      Public Internet
```

---

## Deployment Steps

### 1. Create a New Project

**Option A: Using Railway Dashboard**
1. Go to [Railway Dashboard](https://railway.app/dashboard)
2. Click **"New Project"**
3. Select **"Empty Project"**
4. Name your project (e.g., "heimdall-production")

**Option B: Using Railway CLI**
```bash
railway login
railway init
```

---

### 2. Deploy PostgreSQL

#### Using Railway Dashboard:

1. In your project, click **"+ New"**
2. Select **"Database"** → **"Add PostgreSQL"**
3. Railway will automatically provision a PostgreSQL database
4. Note the connection details (available in the service's **Variables** tab)

#### Connection String Format:
```
postgresql://postgres:[password]@[host]:[port]/railway
```

#### Create Heimdall Database:

Connect to your PostgreSQL instance and create the Heimdall database:

```bash
# Using Railway CLI
railway run psql $DATABASE_URL

# Then run:
CREATE DATABASE heimdall;
```

Or use a PostgreSQL client like TablePlus, DBeaver, or pgAdmin.

---

### 3. Deploy Redis

#### Using Railway Dashboard:

1. Click **"+ New"**
2. Select **"Database"** → **"Add Redis"**
3. Railway will automatically provision a Redis instance
4. Connection details are available in the **Variables** tab

#### Connection String Format:
```
redis://default:[password]@[host]:[port]
```

---

### 4. Deploy FusionAuth

FusionAuth requires its own PostgreSQL database.

#### Step 1: Create FusionAuth PostgreSQL Database

1. In your project, click **"+ New"**
2. Select **"Database"** → **"Add PostgreSQL"**
3. Rename this service to "fusionauth-db" (for clarity)
4. Note the connection details

#### Step 2: Deploy FusionAuth Service

1. Click **"+ New"** → **"Empty Service"**
2. In the service settings:
   - **Name**: `fusionauth`
   - **Source**: Select **"Docker Image"**
   - **Image**: `fusionauth/fusionauth-app:latest`

#### Step 3: Configure FusionAuth Environment Variables

Add the following environment variables to the FusionAuth service:

```bash
DATABASE_URL=jdbc:postgresql://[fusionauth-db-host]:[port]/railway
DATABASE_ROOT_USERNAME=postgres
DATABASE_ROOT_PASSWORD=[fusionauth-db-password]
DATABASE_USERNAME=postgres
DATABASE_PASSWORD=[fusionauth-db-password]
FUSIONAUTH_APP_MEMORY=512M
FUSIONAUTH_APP_RUNTIME_MODE=production
FUSIONAUTH_APP_URL=${{RAILWAY_PUBLIC_DOMAIN}}
SEARCH_TYPE=database
```

#### Step 4: Enable FusionAuth Public Networking

1. Go to FusionAuth service → **Settings**
2. Scroll to **Networking**
3. Click **"Generate Domain"** to create a public URL
4. Note this URL (you'll need it for Heimdall configuration)

#### Step 5: Configure FusionAuth

1. Access FusionAuth at the generated domain
2. Complete the initial setup wizard
3. Create an API key
4. Create a tenant (or use the default)
5. Create an application for Heimdall
6. Note down:
   - API Key
   - Tenant ID
   - Application ID

---

### 5. Deploy Heimdall

#### Step 1: Deploy from GitHub

1. Click **"+ New"** → **"GitHub Repo"**
2. Connect your GitHub account and select your Heimdall repository
3. Railway will auto-detect the Dockerfile

**Or using Railway CLI:**
```bash
railway up
```

#### Step 2: Configure Environment Variables

In the Heimdall service, go to **Variables** and add:

```bash
# Server Configuration
PORT=8080
ENVIRONMENT=production
ALLOWED_ORIGINS=https://your-frontend-domain.com,https://your-app.com
RATE_LIMIT_PER_MIN=100

# Database Configuration
DB_HOST=${{Postgres.PGHOST}}
DB_PORT=${{Postgres.PGPORT}}
DB_USER=${{Postgres.PGUSER}}
DB_PASSWORD=${{Postgres.PGPASSWORD}}
DB_NAME=heimdall
DB_SSLMODE=require
DB_MAX_CONNS=25
DB_MAX_IDLE=5

# Redis Configuration
REDIS_HOST=${{Redis.REDIS_HOST}}
REDIS_PORT=${{Redis.REDIS_PORT}}
REDIS_PASSWORD=${{Redis.REDIS_PASSWORD}}
REDIS_DB=0

# JWT Configuration
JWT_ACCESS_EXPIRY_MIN=15
JWT_REFRESH_EXPIRY_DAYS=7
JWT_ISSUER=heimdall

# FusionAuth Configuration
FUSIONAUTH_URL=${{fusionauth.RAILWAY_PUBLIC_DOMAIN}}
FUSIONAUTH_API_KEY=[your-fusionauth-api-key]
FUSIONAUTH_TENANT_ID=[your-tenant-id]
FUSIONAUTH_APPLICATION_ID=[your-application-id]
OAUTH_REDIRECT_URL=${{RAILWAY_PUBLIC_DOMAIN}}/v1/auth/oauth/callback

# SMTP Configuration (optional - for emails)
SMTP_HOST=[your-smtp-host]
SMTP_PORT=587
SMTP_USERNAME=[your-smtp-username]
SMTP_PASSWORD=[your-smtp-password]
SMTP_FROM=noreply@yourdomain.com

# OAuth Providers (optional)
GOOGLE_CLIENT_ID=[your-google-client-id]
GOOGLE_CLIENT_SECRET=[your-google-client-secret]
GITHUB_CLIENT_ID=[your-github-client-id]
GITHUB_CLIENT_SECRET=[your-github-client-secret]
```

**Note**: Railway automatically provides service references like `${{Postgres.PGHOST}}`. These will be substituted with actual values at runtime.

#### Step 3: Enable Public Networking

1. Go to Heimdall service → **Settings**
2. Scroll to **Networking**
3. Click **"Generate Domain"** to create a public URL
4. Your Heimdall API will be available at: `https://[generated-domain].railway.app`

#### Step 4: Deploy

Railway will automatically deploy on every push to your repository. You can also trigger manual deployments from the dashboard.

---

## Environment Variables

### Required Variables

These are the minimum required environment variables for Heimdall to run:

| Variable | Description | Example |
|----------|-------------|---------|
| `DB_HOST` | PostgreSQL host | `postgres.railway.internal` |
| `DB_PORT` | PostgreSQL port | `5432` |
| `DB_USER` | Database user | `postgres` |
| `DB_PASSWORD` | Database password | `secret123` |
| `DB_NAME` | Database name | `heimdall` |
| `REDIS_HOST` | Redis host | `redis.railway.internal` |
| `REDIS_PORT` | Redis port | `6379` |
| `FUSIONAUTH_URL` | FusionAuth URL | `https://fusionauth.railway.app` |
| `FUSIONAUTH_API_KEY` | FusionAuth API key | `your-api-key` |
| `FUSIONAUTH_TENANT_ID` | FusionAuth tenant ID | `tenant-uuid` |
| `FUSIONAUTH_APPLICATION_ID` | FusionAuth app ID | `app-uuid` |

### Optional Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `PORT` | Server port | `8080` |
| `ENVIRONMENT` | Environment mode | `production` |
| `ALLOWED_ORIGINS` | CORS origins | `*` |
| `RATE_LIMIT_PER_MIN` | Rate limiting | `100` |
| `JWT_ACCESS_EXPIRY_MIN` | JWT access token expiry | `15` |
| `JWT_REFRESH_EXPIRY_DAYS` | JWT refresh token expiry | `7` |

---

## Post-Deployment Configuration

### 1. Verify Deployment

Check that all services are running:

```bash
railway status
```

### 2. Run Migrations

Migrations run automatically on startup via the entrypoint script. To manually trigger:

```bash
railway run ./migrate up
```

### 3. Seed Default Data (Optional)

```bash
railway run ./migrate seed
```

### 4. Test the API

```bash
curl https://your-heimdall-domain.railway.app/health
```

Expected response:
```json
{
  "status": "healthy",
  "timestamp": "2025-10-20T12:00:00Z"
}
```

### 5. Configure FusionAuth Redirect URLs

In FusionAuth dashboard:
1. Go to **Applications** → **Your Application**
2. Under **OAuth**, add:
   - Authorized redirect URLs: `https://your-heimdall-domain.railway.app/v1/auth/oauth/callback`
   - Authorized request origin URLs: `https://your-frontend-domain.com`

---

## Monitoring and Logs

### View Logs

**Using Dashboard:**
1. Go to your service
2. Click on **"Logs"** tab

**Using CLI:**
```bash
railway logs
```

### Metrics

Railway provides built-in metrics:
- CPU usage
- Memory usage
- Network traffic
- Request volume

Access these in the service's **Metrics** tab.

### Health Checks

Heimdall includes a built-in health endpoint:
```
GET /health
```

Railway automatically monitors this endpoint.

---

## Troubleshooting

### Common Issues

#### 1. Database Connection Failed

**Symptom**: `Failed to connect to database`

**Solutions**:
- Verify `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD` are correct
- Ensure `DB_SSLMODE=require` for Railway PostgreSQL
- Check PostgreSQL service is running

#### 2. FusionAuth Connection Failed

**Symptom**: `FusionAuth API error`

**Solutions**:
- Verify `FUSIONAUTH_URL` is accessible
- Check `FUSIONAUTH_API_KEY` is valid
- Ensure FusionAuth service is fully started (takes ~60s)

#### 3. Migrations Failed

**Symptom**: `Migration failed: connection refused`

**Solutions**:
- Wait for database to be fully ready
- Check database credentials
- Manually run migrations: `railway run ./migrate up`

#### 4. Out of Memory

**Symptom**: Service crashes with OOM error

**Solutions**:
- Upgrade Railway plan for more memory
- Reduce `DB_MAX_CONNS` and `DB_MAX_IDLE`
- Optimize FusionAuth memory: `FUSIONAUTH_APP_MEMORY=512M`

### Debug Mode

Enable detailed logging:

```bash
ENVIRONMENT=development
LOG_LEVEL=debug
```

---

## Cost Estimation

Railway pricing (as of 2025):

### Free Tier (Hobby)
- $5 credit per month
- Suitable for development/testing
- All services sleep after inactivity

### Paid Plans (Developer/Team)
- **Starter**: $20/month (included credits)
- **Developer**: $20/month + usage
- **Team**: $99/month + usage

### Estimated Monthly Cost

For a production deployment with:
- 1 Heimdall instance
- 1 PostgreSQL database
- 1 Redis instance
- 1 FusionAuth instance

**Estimated cost**: $20-40/month (depending on traffic)

**Resource usage**:
- Heimdall: ~100MB RAM, minimal CPU
- PostgreSQL: ~200MB RAM
- Redis: ~50MB RAM
- FusionAuth: ~512MB RAM

---

## Alternative: Docker Compose on Railway

If you prefer deploying with docker-compose:

1. Use the provided `docker-compose.yml`
2. Railway doesn't directly support docker-compose, but you can:
   - Deploy services individually
   - Use Railway's internal networking to connect services
   - Reference services using `${{ServiceName.VARIABLE}}`

---

## Production Checklist

Before going live, ensure:

- [ ] All environment variables are set
- [ ] SSL/TLS is enabled (Railway does this by default)
- [ ] Database backups are configured
- [ ] FusionAuth is properly configured with redirect URLs
- [ ] Rate limiting is configured appropriately
- [ ] Monitoring and alerts are set up
- [ ] CORS origins are restricted to your domains
- [ ] SMTP is configured for email notifications
- [ ] OAuth providers are configured (if needed)
- [ ] API keys are secure and not exposed
- [ ] Health checks are passing

---

## Support

For Railway-specific issues:
- [Railway Documentation](https://docs.railway.app/)
- [Railway Discord](https://discord.gg/railway)
- [Railway Support](https://railway.app/help)

For Heimdall issues:
- [GitHub Issues](https://github.com/techsavvyash/heimdall/issues)
- [Documentation](./docs/)

---

## Next Steps

1. ✅ Deploy on Railway using this guide
2. 📚 Review [API Documentation](./docs/API.md)
3. 🔌 Integrate with your application using [SDKs](./docs/SDK.md)
4. 🎯 Check out [Examples](./examples/)

---

**Happy Deploying!** 🚀
