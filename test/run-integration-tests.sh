#!/bin/bash

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}Heimdall Integration Tests${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""

# Check if API is running
API_URL="${HEIMDALL_API_URL:-http://localhost:8080}"
echo -e "${YELLOW}Checking if API is running at ${API_URL}...${NC}"

if ! curl -s -f "${API_URL}/health" > /dev/null 2>&1; then
    echo -e "${RED}❌ API is not running at ${API_URL}${NC}"
    echo -e "${YELLOW}Please start the API with: docker-compose up -d${NC}"
    exit 1
fi

echo -e "${GREEN}✅ API is running${NC}"
echo ""

# Get the directory of this script
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$( cd "${SCRIPT_DIR}/.." && pwd )"

# Change to project root
cd "${PROJECT_ROOT}"

# Run tests
echo -e "${YELLOW}Running integration tests...${NC}"
echo ""

# Export API URL for tests
export HEIMDALL_API_URL="${API_URL}"

# Run Go tests with verbose output
if go test -v ./test/integration/... -timeout 5m; then
    echo ""
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN}✅ All tests passed!${NC}"
    echo -e "${GREEN}========================================${NC}"
    exit 0
else
    echo ""
    echo -e "${RED}========================================${NC}"
    echo -e "${RED}❌ Some tests failed${NC}"
    echo -e "${RED}========================================${NC}"
    exit 1
fi
