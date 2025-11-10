#!/bin/bash

# Register a new user
echo "Registering user..."
REGISTER_RESPONSE=$(curl -s -X POST http://localhost:8080/v1/auth/register \
  -H "Content-Type: application/json" \
  -d "{\"email\":\"pwtest-$(date +%s)@example.com\",\"password\":\"Test123456!\",\"firstName\":\"Test\",\"lastName\":\"User\"}")

echo "$REGISTER_RESPONSE" | jq .

TOKEN=$(echo "$REGISTER_RESPONSE" | jq -r '.data.accessToken')
echo "Token: $TOKEN"

# Try to change password
echo -e "\nChanging password..."
curl -s -X POST http://localhost:8080/v1/auth/password/change \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"currentPassword":"Test123456!","newPassword":"NewPassword123!","confirmPassword":"NewPassword123!"}' | jq .
