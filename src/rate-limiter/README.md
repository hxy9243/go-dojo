# Rate Limiter

## Intro
This project provides a robust, distributed rate-limiting solution implemented in Go. By leveraging Redis as a centralized state store, it ensures consistent rate limiting across multiple service instances. It is designed to be easily integrated into existing Go web applications as a middleware, providing protection against brute-force attacks and API abuse.

## QuickStart

### Prerequisites
- Docker
- Kubernetes cluster (e.g., Minikube or Kind)
- Helm

### Run with Docker
Build the example server image:
```bash
make docker-build
```

### Deploy to Kubernetes
The project includes a complete deployment pipeline that sets up a high-availability Redis cluster (via Sentinel) and the rate-limiting service:
```bash
make deploy
```
This command builds the image, pushes it to the registry, installs Redis via Helm, and applies the service manifests.

## Architecture
The system follows a distributed architecture where multiple application instances share a common rate-limit state stored in Redis.

- **Frontend/Load Balancer:** Forwards requests to application instances.
- **Application Instances:** Run the Go HTTP server with the rate-limiter middleware.
- **Redis Sentinel:** Provides high availability for the Redis backend, ensuring the rate limiter remains functional even if a Redis master fails.
- **Data Store:** Redis Sorted Sets (`ZSET`) are used to track request timestamps per client.

## Implementation

### Redis Sorted Sets & Skiplists
The rate limiter leverages Redis's `ZSET` data structure. Internally, Redis implements `ZSET` using a combination of a **hash table** and a **skip list**.
- The **hash table** provides O(1) lookups for members.
- The **skip list** maintains the order of elements based on their score (in our case, the timestamp), allowing for efficient range-based operations like `ZREMRANGEBYSCORE` in O(log(N) + M) time, where N is the number of elements and M is the number of elements removed.

### Lua Script Implementation
To ensure atomicity and prevent race conditions (check-then-set), the rate-limiting logic is encapsulated in a Lua script executed server-side by Redis:

1. **Window Cleanup:** `ZREMRANGEBYSCORE key 0 <now - window>` removes expired timestamps.
2. **Count:** `ZCARD key` retrieves the number of requests in the current window.
3. **Decision:** If the count is within limits, `ZADD key <now> <unique_value>` adds the current request.
4. **Persistence:** `EXPIRE key <2 * window>` ensures the key eventually expires if no further requests are made.

### Golang Middleware
The middleware wraps standard `http.Handler` instances. It identifies clients using the `X-Client-Id` HTTP header.

- If the header is missing, it returns `400 Bad Request`.
- If the rate limit is exceeded, it returns `429 Too Many Requests`.
- If an internal error occurs (e.g., Redis is down), it returns `500 Internal Server Error`.
- Otherwise, it passes the request to the next handler in the chain.

## Contribute
We welcome contributions to the Rate Limiter project!

You can contribute either by:

- Test and identify bugs and issues. You can create an Issue for any problems you encountered.
- Submit an Issue for Feature Request.
- Create a Pull Request for bug fixes.

Please ensure your code follows standard Go formatting (`go fmt`) and includes appropriate tests.
