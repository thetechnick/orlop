# Orlop - Public API Generator & Server

Orlop is a code generator and API server for Kubernetes-style APIs that creates filtered public API types from private API definitions and serves them via REST endpoints with automatic field filtering and conversion.

## Components

### 1. Code Generator (`orlop-gen`)

Generates filtered public API types from private API definitions using `+orlop:public` markers:

1. Reads Go source files from `apis/private`
2. Filters types and fields based on `+orlop:public` markers
3. Generates cleaned types in `apis/public` maintaining the same directory structure
4. Preserves kubebuilder markers and other non-orlop comments
5. Copies `groupversion_info.go` files as-is
6. Copies `init()` functions that register types into the scheme
7. Copies API version aggregator files with updated import paths
8. Generates DeepCopy methods for both private and public APIs using controller-tools
9. Generates OpenAPI v3 schemas embedded in Go code for both private and public APIs

### 2. API Server (`orlop-server`)

REST API server with dual endpoints:
- **Private API**: Full access to all fields including internal fields
- **Public API**: Filtered view with internal fields hidden from responses
- Automatic conversion between private and public types
- Shared storage backend ensuring consistency
- Schema-based validation, defaulting, and pruning

## Quick Start

### Generate API Types

```bash
# Run the generator
go run ./cmd/orlop-gen

# Or use the Makefile
make generate

# Clean generated files
make clean
```

### Run the API Server

```bash
# Build the server
go build -o bin/orlop-server ./cmd/orlop-server

# Run with both private and public APIs
./bin/orlop-server --private-port 8080 --public-port 8081

# Run with only private API
./bin/orlop-server --private-port 8080 --enable-public-api=false
```

### Command-line Options

**Generator:**
```bash
go run ./cmd/orlop-gen -input-dir=apis/private -output-dir=apis/public
```

**Server:**
```bash
./bin/orlop-server \
  --private-port 8080 \
  --public-port 8081 \
  --enable-public-api=true \
  --cors-origins="*"
```

## Marker Syntax

Mark fields you want to include in the public API with `+orlop:public`:

```go
type ObjectSpec struct {
    // +orlop:public
    PublicField   string `json:"publicField"`
    InternalField string `json:"internalField"`
    
    // +orlop:public
    Nested ObjectNested `json:"nested"`
}
```

## How It Works

### Field Filtering

1. **Direct marking**: Fields with `+orlop:public` are included
2. **Embedded fields**: Anonymous/embedded fields are always included
3. **Type propagation**: Types referenced by public fields are automatically included
4. **List types**: `*List` types for public types are automatically included

### Example

**Input** (`apis/internal/test/v1/object.go`):
```go
type Object struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    
    Spec   ObjectSpec   `json:"spec,omitempty"`
    Status ObjectStatus `json:"status,omitempty"`
}

type ObjectSpec struct {
    // +orlop:public
    PublicField   string `json:"publicField"`
    InternalField string `json:"internalField"`
}
```

**Output** (`apis/public/test/v1/object.go`):
```go
type Object struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    
    Spec   ObjectSpec   `json:"spec,omitempty"`
    Status ObjectStatus `json:"status,omitempty"`
}

type ObjectSpec struct {
    PublicField string `json:"publicField"`
}
```

## Generated Code

The generator produces several generated files:

### DeepCopy Methods (`zz_generated.deepcopy.go`)

Automatically generates DeepCopy, DeepCopyInto, and DeepCopyObject methods for all types in both the internal and public APIs. These methods are required for types that implement `runtime.Object` and are used by the Kubernetes API machinery.

### OpenAPI v3 Schemas (`zz_generated.schemas.go`)

Generates embedded OpenAPI v3 schemas for all CRD types. The schemas are:
- Embedded as YAML constants in Go code
- Pre-parsed into structural schema objects at init time
- Available as both raw YAML and parsed `schema.Structural` objects
- Generated for both internal and public APIs

The schemas include:
- `{TypeName}SchemaYAML` - Raw OpenAPI v3 schema as YAML
- `{TypeName}Schema` - Parsed structural schema
- `{TypeName}Plural` - Plural resource name

These schemas are used for server-side validation and pruning of unknown fields.

## API Server

The API server serves both private and public endpoints with automatic field filtering.

### Architecture

- **Dual Servers**: Separate HTTP servers for private and public APIs
- **Shared Storage**: Both APIs use the same in-memory storage backend
- **Automatic Conversion**: Public API handlers automatically convert between private and public types
- **Field Protection**: Internal fields are never exposed via public API but are preserved in storage

### REST Endpoints

Both servers expose the same endpoint structure:

```
POST   /apis/{group}/{version}/namespaces/{ns}/{resource}           # Create
GET    /apis/{group}/{version}/namespaces/{ns}/{resource}           # List
GET    /apis/{group}/{version}/namespaces/{ns}/{resource}/{name}    # Get
PUT    /apis/{group}/{version}/namespaces/{ns}/{resource}/{name}    # Update
DELETE /apis/{group}/{version}/namespaces/{ns}/{resource}/{name}    # Delete
PUT    /apis/{group}/{version}/namespaces/{ns}/{resource}/{name}/status  # Update status
```

### Private vs Public API

**Private API (port 8080):**
```bash
curl http://localhost:8080/apis/test.orlop.thetechnick.ninja/v1/namespaces/default/objects/test
```
```json
{
  "spec": {
    "publicField": "value",
    "internalField": "internal-value",
    "nested": {
      "publicField": "nested-value",
      "internalField": "nested-internal"
    }
  }
}
```

**Public API (port 8081):**
```bash
curl http://localhost:8081/apis/test.orlop.thetechnick.ninja/v1/namespaces/default/objects/test
```
```json
{
  "spec": {
    "publicField": "value",
    "nested": {
      "publicField": "nested-value"
    }
  }
}
```

### Field Preservation

When updating via the public API, internal fields are preserved:

```bash
# Update via public API (cannot modify internalField)
curl -X PUT http://localhost:8081/.../objects/test \
  -d '{"spec": {"publicField": "updated"}}'

# Verify via private API (internalField preserved)
curl http://localhost:8080/.../objects/test
# Returns: {"spec": {"publicField": "updated", "internalField": "internal-value"}}
```

### Features

- **Schema Validation**: OpenAPI v3 schema validation for all requests
- **Defaulting**: Automatic application of default values from schema
- **Pruning**: Unknown fields removed before storage
- **Generation Tracking**: `.metadata.generation` increments on spec changes
- **Status Subresource**: Independent status updates that preserve spec
- **Optimistic Locking**: ResourceVersion-based concurrency control
- **CORS**: Configurable cross-origin resource sharing

## Directory Structure

```
.
├── apis/
│   ├── private/           # Private API definitions with +orlop:public markers
│   │   └── test/
│   │       ├── test.go    # Version aggregator (SchemeBuilder)
│   │       └── v1/        # v1 API types
│   │           ├── object.go
│   │           ├── other.go
│   │           ├── zz_generated.deepcopy.go    # Generated DeepCopy methods
│   │           └── zz_generated.schemas.go     # Generated OpenAPI schemas
│   └── public/            # Generated public API (git-ignored recommended)
│       └── test/
│           ├── test.go    # Generated aggregator with public imports
│           └── v1/        # Filtered v1 API types
│               ├── object.go                   # Filtered types
│               ├── other.go
│               ├── zz_generated.deepcopy.go
│               ├── zz_generated.schemas.go
│               └── zz_generated.conversion.go  # Generated conversions
├── cmd/
│   ├── orlop-gen/         # Generator CLI
│   └── orlop-server/      # API Server binary
│       └── main.go
├── pkg/
│   ├── generator/         # Generator implementation
│   └── apiserver/         # API Server implementation
│       ├── conversion/    # Private ↔ Public type conversion
│       ├── handlers/      # HTTP request handlers
│       │   ├── resource.go      # Private API handlers
│       │   └── converting.go    # Public API handlers with conversion
│       ├── middleware/    # HTTP middleware (CORS)
│       ├── schema/        # Schema validation/defaulting/pruning
│       ├── storage/       # Storage backend
│       │   ├── interface.go
│       │   └── memory.go  # In-memory storage
│       ├── registry.go    # Resource registration
│       ├── router.go      # HTTP routing
│       └── server.go      # Server lifecycle
└── Makefile
```

## API Version Aggregator

The generator copies aggregator files (like `test.go`) that combine multiple API versions into a single `SchemeBuilder`. Import paths are automatically rewritten to point to the public API:

**Input** (`apis/private/test/test.go`):
```go
import v1 "github.com/thetechnick/orlop/apis/private/test/v1"
```

**Output** (`apis/public/test/test.go`):
```go
import v1 "github.com/thetechnick/orlop/apis/public/test/v1"
```

This allows consumers of the public API to register all types using `test.AddToScheme(scheme)`.

## Testing

Orlop includes comprehensive test coverage with unit tests, integration tests, and PostgreSQL storage backend tests.

### Quick Start

```bash
# Run all tests (including PostgreSQL tests with auto-started database)
make test-all

# Run only unit tests (without database)
make test

# Run only PostgreSQL tests
make test-postgres

# Generate coverage report
make test-coverage
```

### Test Categories

**Unit Tests:**
- In-memory storage and watch (`pkg/apiserver/storage/memory`)
- Type conversion logic (`pkg/apiserver/conversion`)
- Fast, no external dependencies

**Integration Tests:**
- Full API server lifecycle tests (`pkg/integration`)
- Tests cover: CRUD operations, schema validation, defaulting, pruning, status subresource, CORS

**PostgreSQL Tests:**
- Database-backed storage and event broadcaster (`pkg/apiserver/storage/postgres`)
- Requires PostgreSQL (automatically started via Docker with Makefile)
- Tests persistence, multi-instance support, LISTEN/NOTIFY events

### PostgreSQL Test Database

The Makefile manages a PostgreSQL test database automatically:

```bash
# Start PostgreSQL (runs in Docker on port 5433)
make postgres-start

# Stop PostgreSQL (keeps data)
make postgres-stop

# Remove PostgreSQL container completely
make postgres-clean

# View PostgreSQL logs
make postgres-logs

# Connect with psql
make postgres-psql
```

**Configuration:**
- Container: `orlop-test-postgres`
- Port: `5433` (avoids conflicts with local PostgreSQL on 5432)
- Database: `orlop_test`
- Credentials: `orlop` / `orlop_test_password`

See [TESTING.md](./TESTING.md) for detailed testing documentation including CI setup, debugging, and test architecture

### Manual Testing

```bash
# Start the server
./bin/orlop-server --private-port 8080 --public-port 8081

# Create an object via private API
curl -X POST http://localhost:8080/apis/test.orlop.thetechnick.ninja/v1/namespaces/default/objects \
  -H "Content-Type: application/json" \
  -d '{
    "apiVersion": "test.orlop.thetechnick.ninja/v1",
    "kind": "Object",
    "metadata": {"name": "test"},
    "spec": {
      "publicField": "public-value",
      "internalField": "internal-value",
      "nested": {
        "publicField": "nested-public",
        "internalField": "nested-internal"
      }
    }
  }'

# Get via public API (internalField hidden)
curl http://localhost:8081/apis/test.orlop.thetechnick.ninja/v1/namespaces/default/objects/test

# Get via private API (all fields visible)
curl http://localhost:8080/apis/test.orlop.thetechnick.ninja/v1/namespaces/default/objects/test
```

## Implementation Details

### Conversion Layer

The `pkg/apiserver/conversion` package handles bidirectional conversion:

- **PrivateToPublic**: Strips internal fields for public API responses
- **PublicToPrivate**: Preserves existing internal fields from storage when updating

### Handler Types

- **ResourceHandler**: Serves private API with full field access
- **ConvertingResourceHandler**: Serves public API with automatic conversion and field filtering

### Storage

- **Interface**: Abstract storage interface for pluggable backends
- **MemoryStore**: In-memory implementation with thread-safe operations
- ResourceVersion tracking and optimistic concurrency control

### Registry System

Dynamic resource registration allows easy extension:

```go
registry := NewResourceRegistry()
registry.Register(ResourceInfo{
    GVK:           schema.GroupVersionKind{...},
    Plural:        "objects",
    SchemaYAML:    ObjectSchemaYAML,
    NewObjectFunc: func() runtime.Object { return &Object{} },
    NewListFunc:   func() runtime.Object { return &ObjectList{} },
})
```

## License

Apache 2.0
