#!/bin/bash
set -e

echo "🚀 Starting Heimdall..."

# Check if required environment variables are set
if [ -z "$DB_HOST" ]; then
  echo "⚠️  Warning: DB_HOST not set, skipping migrations"
else
  echo "⏳ Waiting for database to be ready..."

  # Wait for database to be ready (max 30 seconds)
  max_attempts=30
  attempt=0

  while [ $attempt -lt $max_attempts ]; do
    if /app/migrate fresh 2>/dev/null; then
      echo "✅ Database migrations and seeding completed successfully"
      break
    else
      attempt=$((attempt + 1))
      if [ $attempt -eq $max_attempts ]; then
        echo "❌ Failed to connect to database after $max_attempts attempts"
        echo "⚠️  Starting server anyway - manual migration may be required"
        break
      fi
      echo "⏳ Attempt $attempt/$max_attempts: Database not ready, waiting..."
      sleep 1
    fi
  done
fi

echo "🎯 Starting Heimdall server..."

# Execute the CMD (server binary or any other command passed)
exec "$@"
