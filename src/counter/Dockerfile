# Use the official Golang image to create a build artifact.
# This is the "builder" stage.
FROM golang:1.25 AS builder

RUN apt-get update && apt-get install -y build-essential librdkafka-dev

# Set the Current Working Directory inside the container
WORKDIR /app

COPY go.mod go.sum ./

# Download all dependencies. Dependencies will be cached if the go.mod and go.sum files are not changed
RUN go mod download

COPY . .

# Build the Go app
# CGO_ENABLED=0 builds a statically linked binary
RUN GOOS=linux go build -o worker cmd/main.go

FROM debian:trixie-slim

RUN apt-get update && apt-get install -y librdkafka-dev

# Copy the built binary from the "builder" stage
COPY --from=builder /app/worker /worker

RUN rm -rf /etc/apt/sources.list.d

# Command to run the executable
ENTRYPOINT ["/worker"]