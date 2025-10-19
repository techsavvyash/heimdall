# CI/CD Documentation

This document describes the CI/CD setup for the Heimdall project.

## Overview

Heimdall uses GitHub Actions for continuous integration and deployment. The workflows are designed to:

1. Build and test the backend on every push and pull request
2. Build and publish the Node.js SDK
3. Run end-to-end tests
4. Create releases with binaries and Docker images
5. Automatically update dependencies

## Workflows

### 1. Backend CI (`backend-ci.yml`)

**Triggers:**
- Push to `main` or `develop` branches
- Pull requests to `main` or `develop` branches

**Jobs:**
- Sets up PostgreSQL and Redis as services
- Runs Go tests with coverage
- Runs golangci-lint for code quality
- Builds the binary
- Uploads coverage to Codecov

**Environment Variables Required:**
- None (uses test environment)

### 2. Node.js SDK CI/CD (`sdk-nodejs-ci.yml`)

**Triggers:**
- Push to `main` or `develop` (when SDK files change)
- Pull requests (when SDK files change)
- Release published

**Jobs:**

#### Build and Test
- Tests on multiple Node.js versions (18.x, 20.x, 21.x)
- Runs linting and type checking
- Builds the SDK
- Runs tests

#### Publish to NPM
- Only runs on release
- Publishes SDK to NPM registry
- Creates GitHub release asset

**Secrets Required:**
- `NPM_TOKEN`: NPM authentication token

### 3. Release Workflow (`release.yml`)

**Triggers:**
- Release published
- Tag pushed (v*)

**Jobs:**

#### Build Binaries
- Builds for multiple platforms:
  - Linux (amd64, arm64)
  - macOS (amd64, arm64)
  - Windows (amd64)
- Creates compressed archives
- Uploads to GitHub release

#### Build Docker Image
- Builds multi-platform Docker images
- Pushes to GitHub Container Registry
- Tags with version and latest

#### Publish SDK
- Publishes Node.js SDK to NPM
- Updates version from release tag

**Secrets Required:**
- `NPM_TOKEN`: NPM authentication token
- `GITHUB_TOKEN`: Automatically provided

### 4. End-to-End Tests (`e2e-tests.yml`)

**Triggers:**
- Push to `main` or `develop`
- Pull requests
- Daily schedule (2 AM UTC)

**Jobs:**
- Sets up complete environment:
  - PostgreSQL
  - Redis
  - FusionAuth
- Builds and starts Heimdall server
- Starts sample application
- Runs API tests
- Tests registration and authentication flow

**Environment Variables:**
- All test environment variables configured automatically

## Dependency Management

### Dependabot Configuration

Automatic dependency updates are configured for:

1. **Go Modules** (weekly, Monday 9 AM)
   - Updates Go dependencies
   - Maximum 5 PRs open at once

2. **Node.js SDK** (weekly, Monday 9 AM)
   - Updates npm dependencies
   - Ignores major TypeScript updates

3. **Sample App** (weekly, Monday 9 AM)
   - Updates example app dependencies

4. **GitHub Actions** (weekly, Monday 9 AM)
   - Updates action versions

5. **Docker** (weekly, Monday 9 AM)
   - Updates base images

### Reviewing Dependency Updates

1. Dependabot creates PRs automatically
2. CI runs on all dependency PRs
3. Review and merge after CI passes
4. Major version updates require manual review

## Setting Up CI/CD

### Required Secrets

Add these secrets in GitHub Settings → Secrets → Actions:

1. **NPM_TOKEN**
   - Generate at npmjs.com
   - Requires publish access
   - Used for publishing SDK

2. **CODECOV_TOKEN** (Optional)
   - Sign up at codecov.io
   - Used for coverage reports

### Required Permissions

Ensure GitHub Actions has these permissions:
- Contents: Read & Write
- Packages: Write
- Pull Requests: Write

### Branch Protection

Recommended branch protection rules for `main`:

- Require pull request reviews (1)
- Require status checks:
  - `Build and Test` (backend-ci)
  - `Build and Test SDK` (sdk-nodejs-ci)
  - `Run E2E Tests` (e2e-tests)
- Require branches to be up to date
- Require conversation resolution

## Release Process

### Creating a Release

1. **Prepare Release**
   ```bash
   # Update CHANGELOG.md
   # Update version in package.json (SDK)
   # Commit changes
   git commit -m "chore: prepare release v1.2.3"
   ```

2. **Create Git Tag**
   ```bash
   git tag -a v1.2.3 -m "Release v1.2.3"
   git push origin v1.2.3
   ```

3. **Create GitHub Release**
   - Go to Releases → Draft a new release
   - Choose the tag (v1.2.3)
   - Generate release notes
   - Publish release

4. **Automated Actions**
   - Binaries are built for all platforms
   - Docker images are published
   - SDK is published to NPM
   - Release assets are uploaded

### Version Numbering

Follow [Semantic Versioning](https://semver.org/):

- **MAJOR** (v2.0.0): Breaking changes
- **MINOR** (v1.1.0): New features, backward compatible
- **PATCH** (v1.0.1): Bug fixes

## Docker Images

### Available Tags

Images are published to `ghcr.io/techsavvyash/heimdall`:

- `latest`: Latest stable release
- `v1.2.3`: Specific version
- `v1.2`: Latest patch of minor version
- `v1`: Latest minor of major version
- `sha-abc123`: Specific commit

### Pulling Images

```bash
docker pull ghcr.io/techsavvyash/heimdall:latest
docker pull ghcr.io/techsavvyash/heimdall:v1.2.3
```

## Monitoring

### CI Status Badges

Add to README.md:

```markdown
![Backend CI](https://github.com/techsavvyash/heimdall/workflows/Backend%20CI/badge.svg)
![Node.js SDK CI](https://github.com/techsavvyash/heimdall/workflows/Node.js%20SDK%20CI%2FCD/badge.svg)
![E2E Tests](https://github.com/techsavvyash/heimdall/workflows/End-to-End%20Tests/badge.svg)
```

### Code Coverage

Coverage reports are uploaded to Codecov on every CI run. View at:
https://codecov.io/gh/techsavvyash/heimdall

## Troubleshooting

### Failed Builds

1. **Check logs** in GitHub Actions tab
2. **Run locally** using same environment
3. **Verify secrets** are configured correctly

### Failed SDK Publication

- Check NPM_TOKEN is valid
- Verify package.json version is unique
- Ensure you're authenticated to NPM

### Failed E2E Tests

- Check service health (PostgreSQL, Redis, FusionAuth)
- Verify all services started successfully
- Check environment variable configuration

## Local Testing

### Test Backend CI Locally

```bash
# Run tests
go test ./...

# Run linter
golangci-lint run

# Build binary
make build
```

### Test SDK Build Locally

```bash
cd sdk/nodejs
npm install
npm run build
npm test
```

### Test Docker Build Locally

```bash
docker build -t heimdall:test .
docker run -p 8080:8080 heimdall:test
```

## Best Practices

1. **Always test locally** before pushing
2. **Keep workflows fast** by caching dependencies
3. **Use matrix builds** for multiple versions
4. **Run E2E tests** before merging to main
5. **Review dependency updates** promptly
6. **Monitor CI failures** and fix quickly
7. **Keep secrets secure** and rotate regularly

## Future Improvements

- [ ] Add performance benchmarks
- [ ] Add security scanning (Snyk, Trivy)
- [ ] Add integration tests for external services
- [ ] Add stress testing
- [ ] Add automatic changelog generation
- [ ] Add release notes automation
