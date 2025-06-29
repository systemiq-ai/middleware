# Use the official Golang Alpine image as the base
FROM golang:1.24-alpine

LABEL org.opencontainers.image.source="https://github.com/systemiq-ai/middleware" \
      org.opencontainers.image.description="Systemiq Middleware Service" \
      org.opencontainers.image.licenses="MIT"
      
# Set the working directory
WORKDIR /app

# Install Air for hot-reloading
RUN go install github.com/air-verse/air@latest

# Copy go.mod and go.sum to download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the entire application source code to the container
COPY . .

# Install additional dependencies required by main.go
RUN go get google.golang.org/grpc \
    google.golang.org/grpc/credentials/insecure

# Expose the gRPC port if needed
EXPOSE 50051

# Define environment variable, with a default value for production
ENV ENVIRONMENT=production

# Use JSON format for CMD to prevent OS signal issues
CMD ["/bin/sh", "-c", "if [ \"$ENVIRONMENT\" = \"development\" ]; then \
      echo 'Starting in development mode with auto-reload...' && \
      air -c .air.toml; \
    else \
      echo 'Starting in production mode...' && \
      go run main.go; \
    fi"]