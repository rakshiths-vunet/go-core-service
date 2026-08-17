# Go Core Service

The `go-core-service` is a minimal, runnable Go backend service that acts as the middle-tier coordinator in the distributed trace test bed. It showcases integration across standard Go HTTP libraries, gRPC communication, and multiple logging frameworks.

## Role in Architecture

The service sits between the static web frontend and the downstream Java backend service.

```
web (browser, :8081) ──[HTTP POST]──> go-core-service (:8090 HTTP, :9090 gRPC) ──[HTTP POST]──> java-order-service (:8080)
                                            │
                                            └──[gRPC loopback]──> (self: ItemLookup)
```

1. **Web Frontend (Browser)** triggers requests directly to `go-core-service` at `/api/orders`.
2. **Go Core Service** coordinates the request:
   - Logs details using both `log/slog` and `sirupsen/logrus`.
   - Calls its own loopback gRPC server (`:9090`) to perform an item lookup (`ItemLookup`).
   - Forwards the request downstream to `java-order-service` via HTTP (`:8080`).
   - Combines downstream and internal results and sends the aggregated JSON response back to the browser.

---

## File Manifest

| File | Purpose |
| :--- | :--- |
| [`cmd/server/main.go`](file:///home/vunet/development/projects/go_test_bed_otelc/go-core-service/cmd/server/main.go) | Entry point. Starts the gRPC and HTTP servers, defines routes, clients, and orchestrates the `/api/orders` request flow. |
| [`internal/server/server.go`](file:///home/vunet/development/projects/go_test_bed_otelc/go-core-service/internal/server/server.go) | Contains the gRPC server implementation for the `CoreServer` interface, handling `ItemLookup` RPCs. |
| [`proto/core.proto`](file:///home/vunet/development/projects/go_test_bed_otelc/go-core-service/proto/core.proto) | Protocol Buffers version 3 schema defining the `Core` service interface and its message formats. |
| [`internal/corepb/core.pb.go`](file:///home/vunet/development/projects/go_test_bed_otelc/go-core-service/internal/corepb/core.pb.go) | Auto-generated protobuf serialization code and structs for `ItemRequest` and `ItemResponse`. |
| [`internal/corepb/core_grpc.pb.go`](file:///home/vunet/development/projects/go_test_bed_otelc/go-core-service/internal/corepb/core_grpc.pb.go) | Auto-generated gRPC server/client interfaces and stubs for the `Core` service. |
| [`go.mod`](file:///home/vunet/development/projects/go_test_bed_otelc/go-core-service/go.mod) | Go module declaration, detailing the module namespace and specifying library dependencies. |
| [`go.sum`](file:///home/vunet/development/projects/go_test_bed_otelc/go-core-service/go.sum) | Cryptographic checksums of direct and indirect module dependencies for build reproducibility. |
| [`.gitignore`](file:///home/vunet/development/projects/go_test_bed_otelc/go-core-service/.gitignore) | Git exclusion list ignoring built binaries, build directories, logs, and development environment files. |

---

## Tech Stack & Covered Libraries

This service is designed to cover standard Go libraries commonly target of distributed tracing and auto-instrumentation agents:

- **Web Routing**: `gin-gonic/gin` (covers Gin HTTP request parsing and response delivery).
- **HTTP client/server**: Standard library `net/http` (used for the `/healthz` endpoint and downstream POST requests).
- **gRPC**: `google.golang.org/grpc` (covers client stubs, gRPC server loopback, and message serialization).
- **Logging**:
  - `log/slog` for structured text stdout logging.
  - `sirupsen/logrus` for standard logging.

---

## Configuration & Ports

- **HTTP Server Address**: `:8090` (Configured in code, exposes endpoints `/healthz` and `/api/orders`).
- **gRPC Server Address**: `:9090` (Configured in code, exposes gRPC service `core.Core`).
- **Downstream URL**: Defaults to `http://localhost:8080` (can be customized via the `JAVA_SERVICE_URL` environment variable).

---

## Run Instructions

From the service directory:

```bash
# Run the Go Core Service
go run ./cmd/server
```

---

## Verification & API Checklist

### 1. Health Check
Checks the plain `net/http` health check endpoint:
```bash
curl http://localhost:8090/healthz
# Expected Response:
# {"status":"ok"}
```

### 2. Submit Order (Gin HTTP Endpoint)
Submits a test order that triggers logging, gRPC lookup, and calls the downstream Java service:
```bash
curl -X POST http://localhost:8090/api/orders \
  -H "Content-Type: application/json" \
  -d '{"item":"widget","qty":5}'
```

**Expected behavior upon request:**
- **Go service stdout** shows:
  - An `slog` entry: `slog.Info("received order request", "item", req.Item, "qty", req.Qty)`
  - A `logrus` entry: `logrus.WithFields(...).Info("received order request")`
  - A gRPC lookup log in server context: `slog.Info("grpc ItemLookup called", "item", req.GetItem())`
  - Downstream call log or error status log.
- **Combined JSON Response**:
  ```json
  {
    "orderId": "<generated-uuid>",
    "item": "widget",
    "qty": 5,
    "processedBy": "go-core-service",
    "triggered": {
      "http": "ok",
      "slog": "ok",
      "logrus": "ok",
      "grpc": "ok",
      "java": "ok"
    },
    "downstream": { ... } // Combined response from java-order-service
  }
  ```
