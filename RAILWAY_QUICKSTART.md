# Railway Deployment Quick Start

Fast-track guide to deploy Heimdall to Railway.app in ~10 minutes.

## Prerequisites

- Railway account ([signup](https://railway.app))
- GitHub account with Heimdall repo
- FusionAuth instance (Cloud or self-hosted)

## Deployment Steps

### 1. Push to GitHub (if not already)

```bash
git add railway.toml .env.railway RAILWAY_DEPLOYMENT.md
git commit -m "Add Railway deployment configuration"
git push origin main
```

### 2. Create Railway Project

1. Go to [railway.app/new](https://railway.app/new)
2. Click **"Deploy from GitHub repo"**
3. Select **heimdall** repository
4. Wait for auto-detection of Dockerfile

### 3. Add Database Plugins

**Add PostgreSQL:**
- Click **"+ New"** → **"Database"** → **"PostgreSQL"**

**Add Redis:**
- Click **"+ New"** → **"Database"** → **"Redis"**

### 4. Set Environment Variables

Go to your Heimdall service → **Variables** → **Raw Editor**, paste:

```bash
ENVIRONMENT=production
DB_SSLMODE=require
DB_MAX_CONNS=25
DB_MAX_IDLE=5
REDIS_DB=0
JWT_PRIVATE_KEY_PATH=/app/keys/private.pem
JWT_PUBLIC_KEY_PATH=/app/keys/public.pem
JWT_ACCESS_EXPIRY_MIN=15
JWT_REFRESH_EXPIRY_DAYS=7
JWT_ISSUER=heimdall
FUSIONAUTH_URL=<your-fusionauth-url>
FUSIONAUTH_API_KEY=<your-api-key>
FUSIONAUTH_TENANT_ID=<your-tenant-id>
FUSIONAUTH_APPLICATION_ID=<your-app-id>
RATE_LIMIT_PER_MIN=100
```

**Important**: Replace FusionAuth placeholders with your actual values.

### 5. Add Database References

In the **Reference Variables** section:
- Add `DATABASE_URL` → Reference → Select PostgreSQL service → `DATABASE_URL`
- Add `REDIS_URL` → Reference → Select Redis service → `REDIS_URL`

### 6. Deploy

Railway will automatically deploy. Monitor in the **Deployments** tab.

### 7. Run Migrations

```bash
# Install Railway CLI
npm i -g @railway/cli

# Login and link
railway login
railway link

# Run migrations
railway run go run cmd/migrate/main.go up
```

### 8. Verify

Get your Railway URL from the dashboard, then:

```bash
# Health check
curl https://your-app.railway.app/health

# Should return:
# {"service":"heimdall","status":"healthy","version":"1.0.0"}
```

### 9. Test Registration

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

### 10. Access Swagger UI

Visit: `https://your-app.railway.app/swagger/`

## Common Issues

**Build fails**: Check Dockerfile and Go version (1.24+)  
**Can't connect to DB**: Verify PostgreSQL plugin is added  
**FusionAuth errors**: Check URL and credentials  
**Health check fails**: Increase timeout in railway.toml  

## Next Steps

- [ ] Configure custom domain
- [ ] Set up monitoring
- [ ] Configure CORS for your frontend
- [ ] Add SMTP for emails
- [ ] Set up backups

## Support

- [Full Documentation](./RAILWAY_DEPLOYMENT.md)
- [Railway Docs](https://docs.railway.app)
- [Railway Discord](https://discord.gg/railway)

---

**Estimated Costs**: $6-10/month on Hobby plan ($5/month with $5 credit included)

