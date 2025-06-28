# Systemiq Middleware

Systemiq Middleware is a compact gRPC service that bridges your local Publisher scripts and the cloud facing Observer endpoint.  
It accepts structured data from the Publisher, validates and enriches it, then forwards the refined payload to Systemiq over its own outbound gRPC channel.  
This extra layer keeps sensitive workloads inside a trusted network while still delivering clean data to analytics and automation services.

## Data Flow

```text
Publisher (local) ► Middleware :50051 ► Systemiq Observer :443

	1.	Publisher gathers and structures data, then sends it to Middleware through a local gRPC call.
	2.	Middleware checks the payload, adds an access token, and forwards it to the external Observer.
	3.	Systemiq receives only validated, well formed data.
```

## Key Features

- gRPC server on port 50051
- Automatic token refresh through AuthHandler
- Client side keepalive for quick detection of half open links
- Graceful reconnect when the Observer becomes unavailable
- Adjustable maximum message size with the OBSERVER_MAX_MSG_SIZE_MB variable

## Requirements

- Go 1.24 or later
- Network reachability to the Observer endpoint (observer.systemiq.ai:443 by default)

## Environment Variables

| Variable                   | Description                                               | Example                          |
|----------------------------|-----------------------------------------------------------|----------------------------------|
| AUTH_EMAIL                 | IAM user email                                            | middleware+1234@systemiq.ai      |
| AUTH_PASSWORD              | Password for the IAM user                                 | supersecret                      |
| AUTH_CLIENT_ID             | Client ID issued by IAM                                   | 2                                |
| OBSERVER_MAX_MSG_SIZE_MB   | Optional size limit in MB for inbound messages            | 4                                |
| TEST_MODE                  | Optional. If true, skips outbound gRPC and returns stub responses   | true                             |

## Local Setup

```bash
go mod tidy
go run ./...
```

If the Observer is offline, Middleware retries every five seconds until a connection is ready.

## Build a Binary

```bash
go build -o observer_middleware main.go
```

## Docker

### Build the image

```bash
docker build -t observer-middleware .
```

### Run in production

```bash
docker run --rm \
  -e AUTH_EMAIL="$AUTH_EMAIL" \
  -e AUTH_PASSWORD="$AUTH_PASSWORD" \
  -e AUTH_CLIENT_ID="$AUTH_CLIENT_ID" \
  -p 50051:50051 \
  observer-middleware
```

### Run in development with hot reload

```bash
docker run --rm \
  -e ENVIRONMENT="development" \
  -e OBSERVER_ENDPOINT="localhost:50052" \
  -v "$(pwd)":/app \
  -p 50051:50051 \
  observer-middleware
```

## License

MIT

---

This service is built for reliable token aware forwarding of telemetry data in containerised or on prem environments.
