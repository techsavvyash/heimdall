#!/bin/bash

# Load all Rego policies into OPA
echo "Loading policies into OPA..."

curl -s -X PUT "http://localhost:8181/v1/policies/authz" --data-binary "@policies/authz.rego"
curl -s -X PUT "http://localhost:8181/v1/policies/rbac" --data-binary "@policies/rbac.rego"
curl -s -X PUT "http://localhost:8181/v1/policies/abac" --data-binary "@policies/abac.rego"
curl -s -X PUT "http://localhost:8181/v1/policies/resource_ownership" --data-binary "@policies/resource_ownership.rego"
curl -s -X PUT "http://localhost:8181/v1/policies/time_based" --data-binary "@policies/time_based.rego"
curl -s -X PUT "http://localhost:8181/v1/policies/tenant_isolation" --data-binary "@policies/tenant_isolation.rego"
curl -s -X PUT "http://localhost:8181/v1/policies/helpers" --data-binary "@policies/helpers.rego"

echo ""
echo "✅ All policies loaded successfully!"
echo ""
echo "Loaded policies:"
curl -s http://localhost:8181/v1/policies | python3 -c "import sys, json; policies = json.load(sys.stdin)['result']; print('\n'.join([p['id'] for p in policies]))"
