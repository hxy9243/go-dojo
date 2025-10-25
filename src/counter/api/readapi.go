package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/gocql/gocql"
	"github.com/google/uuid"

	"github.com/hxy9243/go-dojo/counter/config"
)

type CounterResponse struct {
	Key   string `json:"key"`
	Value int64  `json:"value"`
}

type ReadAPIWorker struct {
	config config.Config

	ID string

	mux         *http.ServeMux
	redisClient *redis.Client
	dbsession   *gocql.Session
}

func newSentinelClient(config config.Config) (*redis.Client, error) {
	redisClient := redis.NewFailoverClient(&redis.FailoverOptions{
		MasterName:       config.RedisMasterName,
		SentinelAddrs:    config.RedisAddr,
		SentinelPassword: config.RedisPassword,
		Password:         config.RedisPassword,
	})

	return redisClient, nil
}

func newStandaloneClient(config config.Config) (*redis.Client, error) {
	redisClient := redis.NewClient(&redis.Options{
		Addr:     config.RedisAddr[0],
		Password: config.RedisPassword,
	})

	return redisClient, nil
}

func NewReadAPIWorker(config config.Config) (*ReadAPIWorker, error) {
	log.Printf("Starting read API workers...")

	mux := http.NewServeMux()

	var (
		redisClient *redis.Client
		dbsession   *gocql.Session
		err         error
	)

	switch config.RedisServiceType {
	case "sentinel":
		redisClient, err = newSentinelClient(config)
		if err != nil {
			return nil, fmt.Errorf("error creating redis client: %w", err)
		}
	case "standalone":
		redisClient, err = newStandaloneClient(config)
		if err != nil {
			return nil, fmt.Errorf("error creating redis client: %w", err)
		}
	default:
		return nil, fmt.Errorf("error creating redis client: unknown service type %s", config.RedisServiceType)
	}

	cluster := gocql.NewCluster(config.CassandraAddr...)
	cluster.Keyspace = config.CassandraKeyspace
	cluster.Consistency = gocql.Quorum
	dbsession, err = cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("error connecting to cassandra: %w", err)
	}

	worker := &ReadAPIWorker{
		config: config,

		ID: uuid.New().String(),

		redisClient: redisClient,
		mux:         mux,
		dbsession:   dbsession,
	}

	worker.initHandlers()
	return worker, nil
}

func (worker *ReadAPIWorker) Serve(addr string) error {
	log.Printf("Starting serving API on %s...", addr)

	return http.ListenAndServe(addr, worker.mux)
}

func (worker *ReadAPIWorker) Close() error {
	worker.dbsession.Close()

	return worker.redisClient.Close()
}

func (worker *ReadAPIWorker) initHandlers() {
	log.Printf("Initializing HTTP handlers...")

	// GET /counter
	worker.mux.HandleFunc("/counter", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]any{"code": http.StatusMethodNotAllowed, "detail": "Method not allowed"})
			return
		}

		vars := r.URL.Query()
		keyVar := vars.Get("key")
		streamVar := vars.Get("stream")

		var (
			stream bool
			err    error
		)
		if keyVar == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"code": http.StatusBadRequest, "detail": "Key value not specified"})
			return
		}

		if streamVar == "" {
			stream = false
		} else {
			stream, err = strconv.ParseBool(streamVar)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{"code": http.StatusBadRequest, "detail": "Error parsing input request"})
				return
			}
		}

		if stream {
			log.Printf("HTTP: streaming response for key %s", keyVar)

			worker.stream(r.Context(), keyVar, r, w)
			return
		} else {
			log.Printf("HTTP: returning response for key %s", keyVar)

			worker.respond(r.Context(), keyVar, r, w)
		}
	})
}

func (worker *ReadAPIWorker) readKey(ctx context.Context, key string) (int64, error) {
	var valStr string

	for {
		log.Printf("reading key value: %s", key)

		var err error
		valStr, err = worker.redisClient.Get(ctx, key).Result()
		if err != nil {
			if err == redis.Nil {
				// if err not exist, lock key in redis
				set, err := worker.redisClient.SetNX(
					ctx,
					key+".counter-lock",
					worker.ID,
					time.Duration(worker.config.RedisCacheTTLms)*time.Millisecond,
				).Result()
				if err != nil {
					log.Printf("Error querying database: %s", err)
					return 0, fmt.Errorf("error locking key: %w", err)
				}
				defer func() {
					if err := worker.redisClient.Del(
						ctx,
						key+".counter-lock",
					).Err(); err != nil {
						log.Printf("Error deleting key: %s", err)
					}
				}()

				if set {
					// query database
					var val int64
					err := worker.dbsession.Query(`SELECT counter FROM counters WHERE key = ?`, key).WithContext(ctx).Scan(&val)
					if err != nil {
						log.Printf("Error querying database: %s", err)
						return 0, fmt.Errorf("error querying database: %w", err)
					}
					// set cache value
					err = worker.redisClient.Set(ctx, key, val, time.Duration(worker.config.RedisCacheTTLms)*time.Millisecond).Err()
					if err != nil {
						log.Printf("Error setting key: %s", err)
						return 0, fmt.Errorf("error setting key: %w", err)
					}
					return val, nil
				} else {
					time.Sleep(10 * time.Millisecond)
					continue
				}
			} else {
				return 0, fmt.Errorf("error reading key: %w", err)
			}
		}
		break
	}

	val, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("error parsing value: %w, getting %s", err, valStr)
	}

	return val, nil
}

func (worker *ReadAPIWorker) watchKey(ctx context.Context, key string) (chan int64, error) {
	ch := make(chan int64)

	return ch, nil
}

// stream the results as SSE back to client
func (worker *ReadAPIWorker) stream(ctx context.Context, keyVar string, r *http.Request, w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch, err := worker.watchKey(ctx, keyVar)
	if err != nil {
		log.Printf("Error watching key: %s", err)

		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{"code": http.StatusInternalServerError, "detail": "Error reading key"})
		return
	}

	flusher := w.(http.Flusher)

	for {
		select {
		case <-r.Context().Done():
			close(ch)
			return
		case val := <-ch:
			buffer := bytes.NewBuffer([]byte{})
			encoder := json.NewEncoder(buffer)

			if err := encoder.Encode(CounterResponse{Key: keyVar, Value: val}); err != nil {
				log.Printf("Error encoding response: %s", err)

				w.WriteHeader(http.StatusInternalServerError)

				json.NewEncoder(w).Encode(map[string]any{"code": http.StatusInternalServerError, "detail": "Error encoding response"})
				return
			}

			if _, err := fmt.Fprintf(w, "data: %s\n\n", buffer.String()); err != nil {
				log.Printf("Error streaming response: %s", err)

				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]any{"code": http.StatusInternalServerError, "detail": "Error streaming response"})
				return
			}
			flusher.Flush()
		}
	}
}

// directly send the result as JSON
func (worker *ReadAPIWorker) respond(ctx context.Context, keyVar string, r *http.Request, w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")

	val, err := worker.readKey(ctx, keyVar)
	if err != nil {
		log.Printf("Error sending response: %s", err)

		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{"code": http.StatusInternalServerError, "detail": "Error reading key"})
		return
	}
	json.NewEncoder(w).Encode(CounterResponse{Key: keyVar, Value: val})
}
