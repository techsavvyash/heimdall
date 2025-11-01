# Deploying Heimdall to Railway.app

This guide walks you through deploying the Heimdall authentication service to Railway.app.

## Prerequisites

1. **Railway Account**: Sign up at [railway.app](https://railway.app)
2. **Railway CLI** (optional): `npm i -g @railway/cli`
3. **GitHub Account**: For connecting your repository
4. **FusionAuth Instance**: Either Railway-hosted or FusionAuth Cloud

---

## Deployment Architecture

```
┌─────────────────────────────────────────┐
│           Railway Project               │
├─────────────────────────────────────────┤
│                                         │
│  ┌────────────────┐  ┌──────────────┐  │
│  │ Heimdall API   │  │ PostgreSQL   │  │
│  │ (Go/Fiber)     │──│ (Plugin)     │  │
│  └────────────────┘  └──────────────┘  │
│         │                               │
│         │            ┌──────────────┐   │
│         └────────────│ Redis        │   │
│                      │ (Plugin)     │   │
│                      └──────────────┘   │
│                                         │
│         │                               │
│         └──────────────────────────────►│
│                                FusionAuth│
│                           (External/Cloud)
└─────────────────────────────────────────┘
```

---

## Step 1: Set Up FusionAuth

### Option A: FusionAuth Cloud (Recommended)

1. Sign up at [fusionauth.io/pricing](https://fusionauth.io/pricing)
2. Create a new application
3. Note your:
   - API URL (e.g., `https://your-instance.fusionauth.io`)
   - API Key
   - Tenant ID
   - Application ID

### Option B: Self-Hosted FusionAuth on Railway

1. Create a new Railway service for FusionAuth
2. Use Docker image: `fusionauth/fusionauth-app:latest`
3. Add PostgreSQL database for FusionAuth
4. Configure environment variables (see FusionAuth docs)
5. Note the internal/public URL

---

## Step 2: Create Railway Project

### Via Railway Dashboard

1. Go to [railway.app/new](https://railway.app/new)
2. Click **"Deploy from GitHub repo"**
3. Select your Heimdall repository
4. Railway will auto-detect the Dockerfile

### Via Railway CLI

```bash
# Login to Railway
railway login

# Initialize project in your repo
cd /path/to/heimdall
railway init

# Link to your project
railway link
```

---

## Step 3: Add Database Services

### Add PostgreSQL

1. In Railway dashboard, click **"+ New"**
2. Select **"Database" → "PostgreSQL"**
3. Railway will automatically create `DATABASE_URL` variable

### Add Redis

1. Click **"+ New"**
2. Select **"Database" → "Redis"**
3. Railway will automatically create `REDIS_URL` variable

---

## Step 4: Configure Environment Variables

In Railway dashboard, go to your Heimdall service → **Variables** tab:

### Required Variables

```bash
# Server Configuration
ENVIRONMENT=production
PORT=${{PORT}}  # Railway provides this automatically
ALLOWED_ORIGINS=https://your-frontend.com

# Database (Auto-configured by Railway PostgreSQL plugin)
DATABASE_URL=${{Postgres.DATABASE_URL}}
# OR manually:
# DB_HOST=${{Postgres.PGHOST}}
# DB_PORT=${{Postgres.PGPORT}}
# DB_USER=${{Postgres.PGUSER}}
# DB_PASSWORD=${{Postgres.PGPASSWORD}}
# DB_NAME=${{Postgres.PGDATABASE}}
DB_SSLMODE=require
DB_MAX_CONNS=25
DB_MAX_IDLE=5

# Redis (Auto-configured by Railway Redis plugin)
REDIS_URL=${{Redis.REDIS_URL}}
# OR manually:
# REDIS_HOST=${{Redis.REDISHOST}}
# REDIS_PORT=${{Redis.REDISPORT}}
# REDIS_PASSWORD=${{Redis.REDISPASSWORD}}
REDIS_DB=0

# JWT Configuration (keys are generated in Dockerfile)
JWT_PRIVATE_KEY_PATH=/app/keys/private.pem
JWT_PUBLIC_KEY_PATH=/app/keys/public.pem
JWT_ACCESS_EXPIRY_MIN=15
JWT_REFRESH_EXPIRY_DAYS=7
JWT_ISSUER=heimdall

# FusionAuth Configuration
FUSIONAUTH_URL=https://your-instance.fusionauth.io
FUSIONAUTH_API_KEY=your-api-key-here
FUSIONAUTH_TENANT_ID=your-tenant-id-here
FUSIONAUTH_APPLICATION_ID=your-application-id-here
OAUTH_REDIRECT_URL=https://your-heimdall-app.railway.app/v1/auth/oauth/callback

# SMTP (Optional - for email features)
SMTP_HOST=smtp.sendgrid.net
SMTP_PORT=587
SMTP_USERNAME=apikey
SMTP_PASSWORD=your-sendgrid-api-key
SMTP_FROM=noreply@yourdomain.com

# Rate Limiting
RATE_LIMIT_PER_MIN=100
```

### Using Railway CLI

```bash
# Set individual variables
railway variables set ENVIRONMENT=production
railway variables set FUSIONAUTH_URL=https://your-instance.fusionauth.io
railway variables set FUSIONAUTH_API_KEY=your-api-key

# Or set from .env file
railway variables set --from-file .env.railway
```

---

## Step 5: Deploy

### Auto-Deploy (Recommended)

Railway automatically deploys when you push to your GitHub repository.

```bash
git add .
git commit -m "Configure for Railway deployment"
git push origin main
```

### Manual Deploy via CLI

```bash
railway up
```

### Check Deployment Status

```bash
# View logs
railway logs

# Check service status
railway status

# Open in browser
railway open
```

---

## Step 6: Run Database Migrations

After first deployment, run migrations:

### Option 1: Via Railway CLI

```bash
# Connect to your Railway project
railway run go run cmd/migrate/main.go up
```

### Option 2: Add Migration Service

Create a one-time job service:
1. Add new service in Railway
2. Use same repo
3. Override start command: `go run cmd/migrate/main.go up`
4. Run once, then delete

### Option 3: Manual Connection

```bash
# Get database connection string
railway variables get DATABASE_URL

# Run migrations locally against Railway DB
DATABASE_URL="postgres://..." go run cmd/migrate/main.go up
```

---

## Step 7: Verify Deployment

### Check Health Endpoint

```bash
curl https://your-app.railway.app/health
```

Expected response:
```json
{
  "service": "heimdall",
  "status": "healthy",
  "version": "1.0.0"
}
```

### Test OpenAPI Documentation

Visit: `https://your-app.railway.app/swagger/`

### Test Registration

```bash
curl -X POST https://your-app.railway.app/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{
    "email": "test@example.com",
    "password": "TestPassword123!",
    "firstName": "Test",
    "lastName": "User"
  }'
```

---

## Step 8: Set Up Custom Domain (Optional)

1. In Railway dashboard, go to Settings
2. Click **"Generate Domain"** for a free Railway domain
3. Or add your **Custom Domain**:
   - Add domain in settings
   - Update DNS records as instructed
   - Railway provides automatic SSL

---

## Monitoring & Maintenance

### View Logs

```bash
# Real-time logs
railway logs --follow

# Filter by service
railway logs --service heimdall-api
```

### Metrics

Railway dashboard provides:
- CPU usage
- Memory usage
- Network traffic
- Response times

### Scaling

Railway automatically scales based on load. Configure in dashboard:
- **Settings → Resources**
- Adjust CPU/Memory limits

### Backups

PostgreSQL and Redis plugins include automatic backups.
Configure in plugin settings.

---

## Troubleshooting

### Build Fails

**Issue**: Docker build fails

**Solutions**:
1. Check Dockerfile syntax
2. Verify Go version compatibility
3. Check build logs in Railway dashboard

### Connection Issues

**Issue**: Can't connect to PostgreSQL/Redis

**Solutions**:
1. Verify plugin is added and healthy
2. Check environment variable references
3. Ensure services are in same project
4. Use internal connection strings (provided automatically)

### FusionAuth Connection Failed

**Issue**: "connection refused" to FusionAuth

**Solutions**:
1. Verify `FUSIONAUTH_URL` is correct
2. Check FusionAuth instance is running
3. Verify API key is valid
4. Check network access/firewall rules

### Health Check Failures

**Issue**: Railway shows service as unhealthy

**Solutions**:
1. Check `/health` endpoint responds
2. Increase `healthcheckTimeout` in railway.toml
3. Check application logs for startup errors
4. Verify all required env vars are set

### High Memory Usage

**Issue**: Service using too much memory

**Solutions**:
1. Reduce `DB_MAX_CONNS` 
2. Optimize Redis usage
3. Check for memory leaks in logs
4. Increase memory allocation in Railway settings

---

## Cost Estimation

Railway pricing (as of 2025):

- **Hobby Plan**: $5/month
  - Includes $5 credit
  - Pay per usage after credit

- **Pro Plan**: $20/month
  - Includes $20 credit
  - Priority support

**Estimated Monthly Costs**:
- Heimdall API: ~$3-5 (small app)
- PostgreSQL: ~$2-3
- Redis: ~$1-2
- **Total**: ~$6-10/month (within Hobby plan)

---

## Production Checklist

Before going to production:

- [ ] FusionAuth configured and tested
- [ ] PostgreSQL plugin added and migrated
- [ ] Redis plugin added and configured
- [ ] All environment variables set
- [ ] Health checks passing
- [ ] Custom domain configured (if needed)
- [ ] SSL/TLS enabled (automatic with custom domain)
- [ ] CORS configured for your frontend
- [ ] Rate limiting configured
- [ ] Monitoring set up
- [ ] Backup strategy confirmed
- [ ] Test authentication flow end-to-end
- [ ] Load testing performed
- [ ] Error tracking configured (optional: Sentry)

---

## Useful Commands

```bash
# Link to existing project
railway link

# View service info
railway status

# Open Railway dashboard
railway open

# Connect to PostgreSQL
railway connect postgres

# Connect to Redis
railway connect redis

# View environment variables
railway variables

# Restart service
railway restart

# Delete service (careful!)
railway down
```

---

## Support & Resources

- **Railway Docs**: [docs.railway.app](https://docs.railway.app)
- **Railway Discord**: [discord.gg/railway](https://discord.gg/railway)
- **FusionAuth Docs**: [fusionauth.io/docs](https://fusionauth.io/docs)
- **Heimdall Issues**: [github.com/techsavvyash/heimdall/issues](https://github.com/techsavvyash/heimdall/issues)

---

## Next Steps

After deployment:

1. **Set up monitoring**: Add Sentry or similar
2. **Configure alerts**: Set up uptime monitoring
3. **Performance tuning**: Optimize based on metrics
4. **Security hardening**: Review security best practices
5. **Documentation**: Document your specific configuration
6. **CI/CD**: Set up automated testing before deploy

---

**Happy Deploying! 🚀**

