# Testing Guide

## Overview

Orlop has comprehensive test coverage including unit tests, integration tests, and database-backed storage tests.

## Test Categories

### 1. Unit Tests
Fast, isolated tests with no external dependencies.

**Packages:**
- `pkg/apiserver/conversion` - Type conversion logic
- `pkg/apiserver/storage/memory` - In-memory storage and watch

**Run:**
```bash
make test-unit
# or
go test -short ./pkg/...
```

### 2. Integration Tests
Tests that exercise the full API server stack.

**Package:**
- `pkg/integration` - End-to-end API server tests

**Run:**
```bash
go test ./pkg/integration/
```

### 3. PostgreSQL Tests
Integration tests for PostgreSQL storage backend.

**Package:**
- `pkg/apiserver/storage/postgres` - PostgreSQL store and broadcaster

**Requirements:**
- PostgreSQL database (automatically started via Docker)
- `POSTGRES_TEST_URL` environment variable

## Quick Start

### Run All Tests (Recommended)

```bash
# Start PostgreSQL and run all tests
make test-all
```

This will:
1. Start a PostgreSQL container via Docker
2. Run all unit and integration tests
3. Run PostgreSQL storage tests
4. Leave PostgreSQL running for future tests

### Run PostgreSQL Tests Only

```bash
# Start PostgreSQL and run database tests
make test-postgres
```

### Run Tests Without Database

```bash
# Run all tests, skip PostgreSQL tests if DB not available
make test
```

PostgreSQL tests will be skipped gracefully with:
```
--- SKIP: TestPostgresStore_Create (0.00s)
    store_test.go:XX: PostgreSQL not available
```

## PostgreSQL Test Database

### Using Docker (Recommended)

The Makefile provides commands to manage a PostgreSQL test database:

```bash
# Start PostgreSQL
make postgres-start

# Stop PostgreSQL (keeps data)
make postgres-stop

# Remove PostgreSQL container
make postgres-clean

# View PostgreSQL logs
make postgres-logs

# Connect to PostgreSQL with psql
make postgres-psql
```

**Default Configuration:**
- Container: `orlop-test-postgres`
- Port: `5433` (to avoid conflicts with other PostgreSQL instances)
- Database: `orlop_test`
- User: `orlop`
- Password: `orlop_test_password`
- URL: `postgres://orlop:orlop_test_password@localhost:5433/orlop_test?sslmode=disable`

### Custom PostgreSQL

Use your own PostgreSQL instance:

```bash
# Export connection string
export POSTGRES_TEST_URL="postgres://user:pass@localhost:5432/testdb?sslmode=disable"

# Run tests
go test ./pkg/apiserver/storage/postgres/
```

### Override Defaults

```bash
# Use different port
make postgres-start POSTGRES_PORT=5434

# Use different credentials
make test-postgres POSTGRES_USER=myuser POSTGRES_PASSWORD=mypass
```

## Test Coverage

### Generate Coverage Report

```bash
make test-coverage
```

This creates:
- `coverage.out` - Raw coverage data
- `coverage.html` - HTML coverage report (open in browser)

### View Coverage by Package

```bash
# With PostgreSQL
POSTGRES_TEST_URL="postgres://..." go test -coverprofile=coverage.out ./pkg/...
go tool cover -func=coverage.out
```

## Running Specific Tests

### By Package

```bash
# Memory storage tests
go test ./pkg/apiserver/storage/memory/

# PostgreSQL storage tests (needs database)
POSTGRES_TEST_URL="postgres://..." go test ./pkg/apiserver/storage/postgres/

# Conversion tests
go test ./pkg/apiserver/conversion/

# Integration tests
go test ./pkg/integration/
```

### By Test Name

```bash
# Run single test
go test -run TestPostgresStore_Create ./pkg/apiserver/storage/postgres/

# Run test suite
go test -run TestPostgresStore ./pkg/apiserver/storage/postgres/

# Verbose output
go test -v -run TestPostgresStore_Create ./pkg/apiserver/storage/postgres/
```

### By Pattern

```bash
# All Create tests
go test -run Create ./pkg/...

# All Watch tests
go test -run Watch ./pkg/...
```

## Continuous Integration

### GitHub Actions Example

```yaml
name: Tests

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    
    services:
      postgres:
        image: postgres:15-alpine
        env:
          POSTGRES_DB: orlop_test
          POSTGRES_USER: orlop
          POSTGRES_PASSWORD: orlop_test_password
        ports:
          - 5432:5432
        options: >-
          --health-cmd pg_isready
          --health-interval 10s
          --health-timeout 5s
          --health-retries 5
    
    steps:
      - uses: actions/checkout@v3
      
      - uses: actions/setup-go@v4
        with:
          go-version: '1.22'
      
      - name: Run tests
        env:
          POSTGRES_TEST_URL: postgres://orlop:orlop_test_password@localhost:5432/orlop_test?sslmode=disable
        run: go test -v ./pkg/...
      
      - name: Upload coverage
        run: |
          go test -coverprofile=coverage.out ./pkg/...
          go tool cover -html=coverage.out -o coverage.html
```

## Test Data Management

### Automatic Cleanup

All tests include cleanup functions:

```go
func TestExample(t *testing.T) {
    store, cleanup := setupTestStore(t)
    defer cleanup() // Always cleans up tables
    
    // Test code...
}
```

### Manual Cleanup

If tests are interrupted, clean up manually:

```bash
# Connect to database
make postgres-psql

# In psql:
DROP TABLE IF EXISTS resources_testobjects CASCADE;
DROP TABLE IF EXISTS event_log CASCADE;
\q
```

Or reset completely:

```bash
make postgres-clean
make postgres-start
```

## Debugging Tests

### Enable Verbose Output

```bash
# Verbose test output
go test -v ./pkg/apiserver/storage/postgres/

# With PostgreSQL logs
make postgres-logs &
go test -v ./pkg/apiserver/storage/postgres/
```

### Debug PostgreSQL Connection

```bash
# Check if PostgreSQL is running
docker ps | grep orlop-test-postgres

# Check PostgreSQL logs
make postgres-logs

# Test connection
docker exec orlop-test-postgres pg_isready -U orlop

# Connect with psql
make postgres-psql
```

### Common Issues

**Issue: Tests skip with "PostgreSQL not available"**
```bash
# Check if database is running
make postgres-start

# Verify connection
psql "postgres://orlop:orlop_test_password@localhost:5433/orlop_test"
```

**Issue: Port already in use**
```bash
# Use different port
make postgres-start POSTGRES_PORT=5434
export POSTGRES_TEST_URL="postgres://orlop:orlop_test_password@localhost:5434/orlop_test?sslmode=disable"
go test ./pkg/apiserver/storage/postgres/
```

**Issue: Tests fail with "connection refused"**
```bash
# Wait for PostgreSQL to be ready
make postgres-start
sleep 2
go test ./pkg/apiserver/storage/postgres/
```

## Test Architecture

### Closure-Based Factories

Tests use closure pattern for flexibility:

```go
// Object builder with options
func newTestObject(opts ...objectOption) *unstructured.Unstructured {
    obj := &unstructured.Unstructured{...}
    for _, opt := range opts {
        opt(obj)
    }
    return obj
}

// Usage
obj := newTestObject(
    withName("test"),
    withNamespace("default"),
    withLabels(map[string]string{"app": "web"}),
)
```

### Test Helpers

Common patterns:

```go
// Setup with cleanup
store, cleanup := setupTestStore(t)
defer cleanup()

// Skip if dependency unavailable
if db == nil {
    t.Skip("PostgreSQL not available")
}

// Parallel tests
t.Run("test name", func(t *testing.T) {
    t.Parallel()
    // Test code...
})
```

## Performance Testing

### Benchmark Tests

```bash
# Run benchmarks
go test -bench=. ./pkg/apiserver/storage/...

# With memory profiling
go test -bench=. -memprofile=mem.out ./pkg/apiserver/storage/...
go tool pprof mem.out
```

### Load Testing

```bash
# Concurrent operations
go test -run TestConcurrency -v ./pkg/apiserver/storage/postgres/
```

## Best Practices

1. **Always use cleanup functions** - Ensures test isolation
2. **Test both success and error paths** - Don't just test happy path
3. **Use table-driven tests** - Makes adding test cases easy
4. **Keep tests fast** - Unit tests should run in milliseconds
5. **Make tests independent** - No shared state between tests
6. **Use descriptive names** - `TestPostgresStore_Create_ReturnsErrorForDuplicate`
7. **Test edge cases** - Empty inputs, nil values, boundary conditions

## Resources

- [Makefile](./Makefile) - All test commands
- [Memory Tests](./pkg/apiserver/storage/memory/store_test.go) - Unit test examples
- [PostgreSQL Tests](./pkg/apiserver/storage/postgres/store_test.go) - Integration test examples
- [Integration Tests](./pkg/integration/) - Full API server tests
