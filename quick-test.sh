#!/bin/bash

# Quick OPA Test - Minimal verification script
# Run this after starting services with: docker compose up -d

set -e

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

echo "🚀 Quick OPA Integration Test"
echo ""

# 1. Check services
echo "1. Checking services..."
curl -sf http://localhost:8080/health > /dev/null && echo -e "${GREEN}✅ Heimdall API${NC}" || echo -e "${RED}❌ Heimdall API${NC}"
curl -sf http://localhost:8181/health > /dev/null && echo -e "${GREEN}✅ OPA${NC}" || echo -e "${RED}❌ OPA${NC}"
echo ""

# 2. Load policies
echo "2. Loading policies..."
./load-policies.sh
echo ""

# 3. Verify policies
echo "3. Verifying policies..."
POLICY_COUNT=$(curl -s http://localhost:8181/v1/policies | jq '.result | length')
echo "   Policies loaded: $POLICY_COUNT/7"
[ "$POLICY_COUNT" -eq 7 ] && echo -e "${GREEN}✅ All policies loaded${NC}" || echo -e "${RED}❌ Missing policies${NC}"
echo ""

# 4. Run integration tests
echo "4. Running integration tests..."
go test -v ./test/integration -run TestOPA -timeout 5m
echo ""

echo -e "${GREEN}✅ Quick test complete!${NC}"
