package main

import (
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/hxy9243/go-dojo/rate-limiter/ratelimit"
)

type ServerConfig struct {
	RedisMasterName   string
	RedisSentinelAddr []string
	RedisPassword     string
	RateLimit         int64
	RateLimitWindow   int64
}

// StartNewExampleServer creates an example server that uses rate limiter
// as a middleware for handling requests
func StartNewExampleServer(config ServerConfig) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello World"))
	})

	// setup rate limiter
	ratelimiter := ratelimit.NewRateLimiterClient(
		ratelimit.RateLimiterOptions{
			MasterName:    config.RedisMasterName,
			SentinelAddrs: config.RedisSentinelAddr,
			Password:      config.RedisPassword,
			Limit:         config.RateLimit,
			Window:        config.RateLimitWindow,
		},
	)

	handler := ratelimiter.Middleware(mux)
	return http.ListenAndServe(":8080", handler)
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getEnvAsInt(name string, fallback int) int {
	valueStr := getEnv(name, "")
	if value, err := strconv.Atoi(valueStr); err == nil {
		return value
	}
	return fallback
}

func main() {
	redisMasterName := getEnv("REDIS_MASTER_NAME", "localhost:6379")
	redisSentinelAddr := getEnv("REDIS_SENTINEL_ADDR", "localhost:26379")
	redisPassword := getEnv("REDIS_PASSWORD", "")
	rateLimit := getEnvAsInt("RATE_LIMIT", 100)
	rateLimitWindow := getEnvAsInt("RATE_LIMIT_WINDOW", 1)

	log.Fatal(StartNewExampleServer(ServerConfig{
		RedisMasterName:   redisMasterName,
		RedisSentinelAddr: []string{redisSentinelAddr},
		RedisPassword:     redisPassword,
		RateLimit:         int64(rateLimit),
		RateLimitWindow:   int64(rateLimitWindow),
	}))
}
