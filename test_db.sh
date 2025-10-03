#!/bin/bash

# Load environment variables
if [ -f .env ]; then
    export $(cat .env | grep -v '^#' | xargs)
fi

echo "🧪 Testing PostgreSQL Database Connection..."
echo ""

# Run the test
go run test_db.go

