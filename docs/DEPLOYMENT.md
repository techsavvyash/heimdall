# Heimdall Deployment Guide

## Overview

This guide covers various deployment options for Heimdall, from local development to production environments using Docker, Kubernetes, and cloud platforms.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Local Development](#local-development)
- [Docker Deployment](#docker-deployment)
- [Kubernetes Deployment](#kubernetes-deployment)
- [Cloud Platform Deployment](#cloud-platform-deployment)
- [Configuration](#configuration)
- [Monitoring & Logging](#monitoring--logging)
- [Backup & Disaster Recovery](#backup--disaster-recovery)
- [Security Considerations](#security-considerations)

---

## Prerequisites

### Required Services

1. **PostgreSQL 14+**: For Heimdall application data
2. **Redis 7+**: For caching and session storage
3. **FusionAuth**: Core authentication engine
4. **SMTP Server**: For sending emails (optional but recommended)

### System Requirements

**Minimum (Development):**
- CPU: 2 cores
- RAM: 4GB
- Disk: 20GB

**Recommended (Production):**
- CPU: 4+ cores
- RAM: 8GB+
- Disk: 100GB+ (SSD recommended)

---

## Local Development

### 1. Clone Repository

```bash
git clone https://github.com/techsavvyash/heimdall.git
cd heimdall
```

### 2. Install Dependencies

```bash
# Install Go dependencies
go mod download

# For SDK development
cd sdk/js
npm install
```

### 3. Start Dependencies with Docker Compose

```bash
# Start PostgreSQL, Redis, and FusionAuth
docker-compose -f docker-compose.dev.yml up -d
```

**docker-compose.dev.yml:**
```yaml
version: '3.8'

services:
  postgres:
    image: postgres:14
    environment:
      POSTGRES_USER: heimdall
      POSTGRES_PASSWORD: heimdall_dev
      POSTGRES_DB: heimdall
    ports:
      - "5432:5432"
    volumes:
      - postgres_data:/var/lib/postgresql/data

  redis:
    image: redis:7-alpine
    ports:
      - "6379:6379"
    volumes:
      - redis_data:/data

  fusionauth:
    image: fusionauth/fusionauth-app:latest
    depends_on:
      - postgres
    environment:
      DATABASE_URL: jdbc:postgresql://postgres:5432/fusionauth
      DATABASE_ROOT_USERNAME: heimdall
      DATABASE_ROOT_PASSWORD: heimdall_dev
      FUSIONAUTH_APP_MEMORY: 512M
    ports:
      - "9011:9011"
    volumes:
      - fusionauth_data:/usr/local/fusionauth/config

volumes:
  postgres_data:
  redis_data:
  fusionauth_data:
```

### 4. Configure Environment Variables

Create a `.env` file:

```bash
# Server Configuration
PORT=8080
ENV=development
LOG_LEVEL=debug

# Database
DATABASE_URL=postgresql://heimdall:heimdall_dev@localhost:5432/heimdall?sslmode=disable

# Redis
REDIS_URL=redis://localhost:6379/0

# FusionAuth
FUSIONAUTH_URL=http://localhost:9011
FUSIONAUTH_API_KEY=your-fusionauth-api-key
FUSIONAUTH_TENANT_ID=default

# JWT Configuration
JWT_PRIVATE_KEY_PATH=./keys/private.pem
JWT_PUBLIC_KEY_PATH=./keys/public.pem
JWT_ISSUER=https://heimdall.localhost

# Token Lifetimes (seconds)
ACCESS_TOKEN_LIFETIME=900        # 15 minutes
REFRESH_TOKEN_LIFETIME=2592000   # 30 days

# SMTP Configuration (optional)
SMTP_HOST=smtp.mailtrap.io
SMTP_PORT=2525
SMTP_USERNAME=your-username
SMTP_PASSWORD=your-password
SMTP_FROM=noreply@heimdall.local

# Rate Limiting
RATE_LIMIT_REQUESTS_PER_HOUR=1000
RATE_LIMIT_ANONYMOUS_REQUESTS_PER_HOUR=100

# CORS
CORS_ALLOWED_ORIGINS=http://localhost:3000,http://localhost:3001
```

### 5. Generate JWT Keys

```bash
# Generate RSA key pair for JWT signing
mkdir -p keys
openssl genrsa -out keys/private.pem 2048
openssl rsa -in keys/private.pem -pubout -out keys/public.pem
```

### 6. Run Database Migrations

```bash
go run cmd/migrate/main.go up
```

### 7. Start Server

```bash
go run cmd/server/main.go
```

The API will be available at `http://localhost:8080`.

---

## Docker Deployment

### Single Container Deployment

**Dockerfile:**
```dockerfile
# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Install dependencies
RUN apk add --no-cache git make

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o heimdall ./cmd/server

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy binary from builder
COPY --from=builder /app/heimdall .

# Copy migrations
COPY --from=builder /app/migrations ./migrations

# Expose port
EXPOSE 8080

# Run
CMD ["./heimdall"]
```

**Build and Run:**
```bash
# Build image
docker build -t heimdall:latest .

# Run container
docker run -d \
  --name heimdall \
  -p 8080:8080 \
  --env-file .env \
  heimdall:latest
```

### Full Stack Docker Compose

**docker-compose.yml:**
```yaml
version: '3.8'

services:
  heimdall:
    build: .
    ports:
      - "8080:8080"
    environment:
      DATABASE_URL: postgresql://heimdall:${POSTGRES_PASSWORD}@postgres:5432/heimdall
      REDIS_URL: redis://redis:6379/0
      FUSIONAUTH_URL: http://fusionauth:9011
      JWT_ISSUER: https://heimdall.yourdomain.com
    env_file:
      - .env
    depends_on:
      - postgres
      - redis
      - fusionauth
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "--quiet", "--tries=1", "--spider", "http://localhost:8080/health"]
      interval: 30s
      timeout: 10s
      retries: 3

  postgres:
    image: postgres:14
    environment:
      POSTGRES_USER: heimdall
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: heimdall
    volumes:
      - postgres_data:/var/lib/postgresql/data
    restart: unless-stopped
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U heimdall"]
      interval: 10s
      timeout: 5s
      retries: 5

  redis:
    image: redis:7-alpine
    command: redis-server --appendonly yes
    volumes:
      - redis_data:/data
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 10s
      timeout: 5s
      retries: 5

  fusionauth:
    image: fusionauth/fusionauth-app:latest
    depends_on:
      - postgres
    environment:
      DATABASE_URL: jdbc:postgresql://postgres:5432/fusionauth
      DATABASE_ROOT_USERNAME: heimdall
      DATABASE_ROOT_PASSWORD: ${POSTGRES_PASSWORD}
      FUSIONAUTH_APP_MEMORY: 1G
      FUSIONAUTH_APP_RUNTIME_MODE: production
    ports:
      - "9011:9011"
    volumes:
      - fusionauth_data:/usr/local/fusionauth/config
    restart: unless-stopped

  nginx:
    image: nginx:alpine
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
      - ./ssl:/etc/nginx/ssl:ro
    depends_on:
      - heimdall
    restart: unless-stopped

volumes:
  postgres_data:
  redis_data:
  fusionauth_data:
```

**Start Stack:**
```bash
docker-compose up -d
```

---

## Kubernetes Deployment

### Prerequisites

- Kubernetes cluster (v1.24+)
- kubectl configured
- Helm 3 (optional)

### 1. Create Namespace

```bash
kubectl create namespace heimdall
```

### 2. Create Secrets

```bash
# Database credentials
kubectl create secret generic heimdall-db \
  --from-literal=username=heimdall \
  --from-literal=password=your-secure-password \
  -n heimdall

# JWT keys
kubectl create secret generic heimdall-jwt \
  --from-file=private.pem=./keys/private.pem \
  --from-file=public.pem=./keys/public.pem \
  -n heimdall

# FusionAuth API key
kubectl create secret generic fusionauth \
  --from-literal=api-key=your-fusionauth-api-key \
  -n heimdall
```

### 3. Deploy PostgreSQL

**postgres-deployment.yaml:**
```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: postgres-pvc
  namespace: heimdall
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 50Gi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgres
  namespace: heimdall
spec:
  replicas: 1
  selector:
    matchLabels:
      app: postgres
  template:
    metadata:
      labels:
        app: postgres
    spec:
      containers:
      - name: postgres
        image: postgres:14
        env:
        - name: POSTGRES_DB
          value: heimdall
        - name: POSTGRES_USER
          valueFrom:
            secretKeyRef:
              name: heimdall-db
              key: username
        - name: POSTGRES_PASSWORD
          valueFrom:
            secretKeyRef:
              name: heimdall-db
              key: password
        ports:
        - containerPort: 5432
        volumeMounts:
        - name: postgres-storage
          mountPath: /var/lib/postgresql/data
      volumes:
      - name: postgres-storage
        persistentVolumeClaim:
          claimName: postgres-pvc
---
apiVersion: v1
kind: Service
metadata:
  name: postgres
  namespace: heimdall
spec:
  selector:
    app: postgres
  ports:
  - port: 5432
    targetPort: 5432
```

### 4. Deploy Redis

**redis-deployment.yaml:**
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis
  namespace: heimdall
spec:
  replicas: 1
  selector:
    matchLabels:
      app: redis
  template:
    metadata:
      labels:
        app: redis
    spec:
      containers:
      - name: redis
        image: redis:7-alpine
        command: ["redis-server", "--appendonly", "yes"]
        ports:
        - containerPort: 6379
        volumeMounts:
        - name: redis-storage
          mountPath: /data
      volumes:
      - name: redis-storage
        emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: redis
  namespace: heimdall
spec:
  selector:
    app: redis
  ports:
  - port: 6379
    targetPort: 6379
```

### 5. Deploy Heimdall

**heimdall-deployment.yaml:**
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: heimdall
  namespace: heimdall
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
        image: techsavvyash/heimdall:latest
        ports:
        - containerPort: 8080
        env:
        - name: PORT
          value: "8080"
        - name: ENV
          value: "production"
        - name: DATABASE_URL
          value: "postgresql://$(DB_USERNAME):$(DB_PASSWORD)@postgres:5432/heimdall"
        - name: DB_USERNAME
          valueFrom:
            secretKeyRef:
              name: heimdall-db
              key: username
        - name: DB_PASSWORD
          valueFrom:
            secretKeyRef:
              name: heimdall-db
              key: password
        - name: REDIS_URL
          value: "redis://redis:6379/0"
        - name: FUSIONAUTH_URL
          value: "http://fusionauth:9011"
        - name: FUSIONAUTH_API_KEY
          valueFrom:
            secretKeyRef:
              name: fusionauth
              key: api-key
        - name: JWT_PRIVATE_KEY_PATH
          value: "/keys/private.pem"
        - name: JWT_PUBLIC_KEY_PATH
          value: "/keys/public.pem"
        volumeMounts:
        - name: jwt-keys
          mountPath: /keys
          readOnly: true
        resources:
          requests:
            memory: "256Mi"
            cpu: "250m"
          limits:
            memory: "512Mi"
            cpu: "500m"
        livenessProbe:
          httpGet:
            path: /health/live
            port: 8080
          initialDelaySeconds: 30
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /health/ready
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 5
      volumes:
      - name: jwt-keys
        secret:
          secretName: heimdall-jwt
---
apiVersion: v1
kind: Service
metadata:
  name: heimdall
  namespace: heimdall
spec:
  selector:
    app: heimdall
  ports:
  - port: 80
    targetPort: 8080
  type: ClusterIP
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: heimdall-hpa
  namespace: heimdall
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: heimdall
  minReplicas: 3
  maxReplicas: 10
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
  - type: Resource
    resource:
      name: memory
      target:
        type: Utilization
        averageUtilization: 80
```

### 6. Ingress Configuration

**ingress.yaml:**
```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: heimdall-ingress
  namespace: heimdall
  annotations:
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
    nginx.ingress.kubernetes.io/ssl-redirect: "true"
    nginx.ingress.kubernetes.io/rate-limit: "100"
spec:
  ingressClassName: nginx
  tls:
  - hosts:
    - api.heimdall.yourdomain.com
    secretName: heimdall-tls
  rules:
  - host: api.heimdall.yourdomain.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: heimdall
            port:
              number: 80
```

### 7. Deploy All Resources

```bash
kubectl apply -f postgres-deployment.yaml
kubectl apply -f redis-deployment.yaml
kubectl apply -f heimdall-deployment.yaml
kubectl apply -f ingress.yaml
```

### 8. Verify Deployment

```bash
# Check pods
kubectl get pods -n heimdall

# Check services
kubectl get svc -n heimdall

# Check ingress
kubectl get ingress -n heimdall

# View logs
kubectl logs -f deployment/heimdall -n heimdall
```

---

## Cloud Platform Deployment

### AWS (EKS)

#### 1. Create EKS Cluster

```bash
eksctl create cluster \
  --name heimdall-cluster \
  --region us-west-2 \
  --nodegroup-name standard-workers \
  --node-type t3.medium \
  --nodes 3 \
  --nodes-min 3 \
  --nodes-max 10 \
  --managed
```

#### 2. Use RDS for PostgreSQL

```bash
# Create RDS instance via AWS Console or CLI
aws rds create-db-instance \
  --db-instance-identifier heimdall-postgres \
  --db-instance-class db.t3.medium \
  --engine postgres \
  --master-username heimdall \
  --master-user-password your-secure-password \
  --allocated-storage 100
```

#### 3. Use ElastiCache for Redis

```bash
aws elasticache create-cache-cluster \
  --cache-cluster-id heimdall-redis \
  --cache-node-type cache.t3.medium \
  --engine redis \
  --num-cache-nodes 1
```

#### 4. Update Database URLs

Update your Kubernetes secrets with RDS and ElastiCache endpoints.

### Google Cloud (GKE)

#### 1. Create GKE Cluster

```bash
gcloud container clusters create heimdall-cluster \
  --zone us-central1-a \
  --num-nodes 3 \
  --machine-type n1-standard-2 \
  --enable-autoscaling \
  --min-nodes 3 \
  --max-nodes 10
```

#### 2. Use Cloud SQL

```bash
gcloud sql instances create heimdall-postgres \
  --database-version=POSTGRES_14 \
  --tier=db-custom-2-7680 \
  --region=us-central1
```

#### 3. Use Memorystore for Redis

```bash
gcloud redis instances create heimdall-redis \
  --size=1 \
  --region=us-central1 \
  --redis-version=redis_7_0
```

### Azure (AKS)

#### 1. Create AKS Cluster

```bash
az aks create \
  --resource-group heimdall-rg \
  --name heimdall-cluster \
  --node-count 3 \
  --enable-addons monitoring \
  --generate-ssh-keys
```

#### 2. Use Azure Database for PostgreSQL

```bash
az postgres server create \
  --resource-group heimdall-rg \
  --name heimdall-postgres \
  --sku-name GP_Gen5_2 \
  --version 14
```

#### 3. Use Azure Cache for Redis

```bash
az redis create \
  --resource-group heimdall-rg \
  --name heimdall-redis \
  --sku Basic \
  --vm-size c0
```

---

## Configuration

### Environment Variables Reference

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `PORT` | Server port | 8080 | No |
| `ENV` | Environment (development, production) | development | No |
| `LOG_LEVEL` | Logging level (debug, info, warn, error) | info | No |
| `DATABASE_URL` | PostgreSQL connection string | - | Yes |
| `REDIS_URL` | Redis connection string | - | Yes |
| `FUSIONAUTH_URL` | FusionAuth API URL | - | Yes |
| `FUSIONAUTH_API_KEY` | FusionAuth API key | - | Yes |
| `JWT_PRIVATE_KEY_PATH` | Path to JWT private key | - | Yes |
| `JWT_PUBLIC_KEY_PATH` | Path to JWT public key | - | Yes |
| `JWT_ISSUER` | JWT issuer claim | - | Yes |
| `ACCESS_TOKEN_LIFETIME` | Access token TTL (seconds) | 900 | No |
| `REFRESH_TOKEN_LIFETIME` | Refresh token TTL (seconds) | 2592000 | No |
| `SMTP_HOST` | SMTP server host | - | No |
| `SMTP_PORT` | SMTP server port | 587 | No |
| `CORS_ALLOWED_ORIGINS` | Comma-separated allowed origins | * | No |
| `RATE_LIMIT_REQUESTS_PER_HOUR` | Rate limit for auth users | 1000 | No |

---

## Monitoring & Logging

### Prometheus Metrics

Heimdall exposes Prometheus metrics at `/metrics`:

```yaml
# ServiceMonitor for Prometheus Operator
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: heimdall
  namespace: heimdall
spec:
  selector:
    matchLabels:
      app: heimdall
  endpoints:
  - port: http
    path: /metrics
    interval: 30s
```

**Key Metrics:**
- `heimdall_http_requests_total`: Total HTTP requests
- `heimdall_http_request_duration_seconds`: Request latency
- `heimdall_auth_login_attempts_total`: Login attempts
- `heimdall_auth_login_failures_total`: Failed login attempts
- `heimdall_token_generations_total`: Tokens generated
- `heimdall_cache_hits_total`: Cache hits
- `heimdall_cache_misses_total`: Cache misses

### Logging

Heimdall outputs structured JSON logs:

```json
{
  "level": "info",
  "timestamp": "2024-01-15T10:30:00Z",
  "message": "User logged in",
  "userId": "550e8400-e29b-41d4-a716-446655440000",
  "tenantId": "tenant-123",
  "ipAddress": "192.168.1.1",
  "userAgent": "Mozilla/5.0...",
  "requestId": "req-abc123"
}
```

### ELK Stack Integration

**filebeat-config.yaml:**
```yaml
filebeat.inputs:
- type: container
  paths:
    - '/var/lib/docker/containers/*/*.log'
  processors:
  - add_kubernetes_metadata:
      host: ${NODE_NAME}
      matchers:
      - logs_path:
          logs_path: "/var/lib/docker/containers/"

output.elasticsearch:
  hosts: ['${ELASTICSEARCH_HOST:elasticsearch}:${ELASTICSEARCH_PORT:9200}']
```

---

## Backup & Disaster Recovery

### Database Backup

#### Automated PostgreSQL Backup

```bash
# Backup script
#!/bin/bash
DATE=$(date +%Y%m%d_%H%M%S)
BACKUP_DIR=/backups
pg_dump -h localhost -U heimdall heimdall | gzip > $BACKUP_DIR/heimdall_$DATE.sql.gz

# Keep last 30 days
find $BACKUP_DIR -name "heimdall_*.sql.gz" -mtime +30 -delete
```

#### Kubernetes CronJob for Backups

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: postgres-backup
  namespace: heimdall
spec:
  schedule: "0 2 * * *"  # Daily at 2 AM
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: backup
            image: postgres:14
            command:
            - /bin/sh
            - -c
            - pg_dump -h postgres -U heimdall heimdall | gzip > /backups/backup_$(date +%Y%m%d_%H%M%S).sql.gz
            env:
            - name: PGPASSWORD
              valueFrom:
                secretKeyRef:
                  name: heimdall-db
                  key: password
            volumeMounts:
            - name: backups
              mountPath: /backups
          restartPolicy: OnFailure
          volumes:
          - name: backups
            persistentVolumeClaim:
              claimName: backup-pvc
```

### Disaster Recovery Plan

1. **Regular Backups**: Daily database backups stored in S3/GCS
2. **Multi-Region**: Deploy in multiple regions for high availability
3. **Data Replication**: PostgreSQL streaming replication
4. **Monitoring**: 24/7 monitoring with PagerDuty alerts
5. **Recovery Time Objective (RTO)**: < 1 hour
6. **Recovery Point Objective (RPO)**: < 5 minutes

---

## Security Considerations

### 1. Network Security

- Use private subnets for databases
- Enable VPC peering/private links
- Configure security groups/firewall rules
- Use TLS for all communications

### 2. Secrets Management

```bash
# Use external secrets operator
kubectl apply -f https://raw.githubusercontent.com/external-secrets/external-secrets/main/deploy/crds/bundle.yaml

# AWS Secrets Manager integration
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata:
  name: aws-secrets
  namespace: heimdall
spec:
  provider:
    aws:
      service: SecretsManager
      region: us-west-2
```

### 3. Regular Updates

- Keep dependencies updated
- Apply security patches promptly
- Regular vulnerability scanning

### 4. Access Control

- Use RBAC for Kubernetes
- Implement least privilege principle
- Enable audit logging

---

## Performance Tuning

### Database Optimization

```sql
-- Create indexes
CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_audit_logs_tenant_time ON audit_logs(tenant_id, created_at DESC);

-- Configure PostgreSQL
shared_buffers = 256MB
effective_cache_size = 1GB
maintenance_work_mem = 64MB
max_connections = 200
```

### Redis Optimization

```conf
maxmemory 2gb
maxmemory-policy allkeys-lru
save 900 1
save 300 10
save 60 10000
```

### Application Tuning

```bash
# Go runtime settings
GOMAXPROCS=4
GOGC=100
```

---

## Troubleshooting

### Common Issues

#### Connection to PostgreSQL Failed
```bash
# Check connectivity
kubectl exec -it heimdall-pod -- nc -zv postgres 5432

# Check credentials
kubectl get secret heimdall-db -o yaml
```

#### High Memory Usage
```bash
# Check pod resources
kubectl top pods -n heimdall

# Increase limits if needed
kubectl set resources deployment heimdall --limits=memory=1Gi
```

#### Rate Limiting Issues
```bash
# Check rate limit configuration
# Adjust RATE_LIMIT_REQUESTS_PER_HOUR in environment
```

---

## Conclusion

This deployment guide covers the essential steps for deploying Heimdall in various environments. For specific use cases or advanced configurations, refer to the [Architecture documentation](./ARCHITECTURE.md) or contact support.
