# Systemiq Middleware

Systemiq Middleware is a compact gRPC bridge that lets your local Publisher
scripts forward telemetry to the cloud-facing Observer service without exposing
internal networks.  
It accepts structured data from the Publisher, attaches a fresh JWT, and
forwards the payload to **Observer** over a single, self-healing gRPC channel.

## Data Flow

```

Publisher (local) ──► Middleware  :50051 ──► Observer  :443

1. Publisher builds a gRPC `ObservationRequest` and sends it locally.
2. Middleware validates the data, fetches/refreshes a JWT, and forwards it.
3. Observer receives only authenticated, well-formed messages.

````

## Key Features

| Feature | Notes |
|---------|-------|
| **gRPC server on port 50051** | Receives `ObservationRequest` from local publishers |
| **Single persistent client conn** | gRPC’s native reconnection & back-off (no custom loops) |
| **Keep-alive pings** | Detects half-open TCP links even when idle |
| **Automatic JWT refresh** | Background `AuthHandler` renews tokens before expiry |
| **Configurable max msg size** | `OBSERVER_MAX_MSG_SIZE_MB` (default 4 MiB) |
| **Test mode** | `TEST_MODE=true` skips outbound Observer calls |

## Requirements

* **Go ≥ 1.24**
* Egress access to the Observer endpoint  
  (`observer.systemiq.ai:443` by default)

## Environment Variables

| Variable | Description | Example |
|----------|-------------|---------|
| `AUTH_EMAIL` | IAM user email | `middleware@systemiq.ai` |
| `AUTH_PASSWORD` | Password for the IAM user | `supersecret` |
| `AUTH_CLIENT_ID` | Client ID issued by IAM | `2` |
| `AUTH_LOGIN_ENDPOINT` | *(optional)* override login URL | `https://api.systemiq.ai/auth/login` |
| `AUTH_REFRESH_ENDPOINT` | *(optional)* token-refresh URL | `https://api.systemiq.ai/auth/refresh-token` |
| `OBSERVER_ENDPOINT` | *(optional)* gRPC target (defaults to `observer.systemiq.ai:443`) | `localhost:50052` |
| `OBSERVER_MAX_MSG_SIZE_MB` | *(optional)* size limit for in/out messages | `8` |
| `TEST_MODE` | *(optional)* `true`/`1` to stub-out Observer calls | `true` |

## Quick Start (Local)

```bash
go mod tidy
go run ./...
````

If Observer is offline, gRPC retries with exponential back-off until it becomes
`READY`; your call waits up to 5 s (`context.WithTimeout`) and returns
`codes.Unavailable` if still down.

## Build Binary

```bash
go build -o observer_middleware main.go
```

## Docker

### Build

```bash
docker build -t observer-middleware .
```

### Run (Production)

```bash
docker run --rm \
  -e AUTH_EMAIL="$AUTH_EMAIL" \
  -e AUTH_PASSWORD="$AUTH_PASSWORD" \
  -e AUTH_CLIENT_ID="$AUTH_CLIENT_ID" \
  -p 50051:50051 \
  observer-middleware
```

### Run (Development / hot-reload)

```bash
docker run --rm \
  -e ENVIRONMENT=development \
  -e OBSERVER_ENDPOINT="localhost:50052" \
  -v "$(pwd)":/app \
  -p 50051:50051 \
  observer-middleware
```

## Pre-built Image

```bash
docker pull ghcr.io/systemiq-ai/middleware:latest
```

## License

MIT © [systemiq.ai](https://systemiq.ai)