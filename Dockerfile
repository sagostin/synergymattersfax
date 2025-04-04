# Start from a Golang base image
FROM golang:1.24.1-bookworm AS builder

# Configure Go proxy
ENV GOPROXY=https://proxy.golang.org,direct

# Set the working directory inside the container
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies
RUN go mod download

# Copy the source code into the container
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

# Start a new stage with Ubuntu
FROM ubuntu:latest

# Install necessary packages
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl tzdata && \
    rm -rf /var/lib/apt/lists/*

# Create a non-root user
RUN useradd -m appuser

# Set the working directory
WORKDIR /app

# Copy the pre-built binary file from the previous stage
COPY --from=builder /app/main .

# Use the non-root user
USER appuser

# Expose port 3000
EXPOSE 3000

# Command to run the executable
CMD ["./main"]