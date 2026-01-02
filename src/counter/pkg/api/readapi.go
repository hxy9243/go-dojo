package api

import (
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

	"github.com/hxy9243/go-dojo/counter/pkg/config"
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
			if err := json.NewEncoder(w).Encode(map[string]any{"code": http.StatusMethodNotAllowed, "detail": "Method not allowed"}); err != nil {
				log.Printf("Error encoding response: %s", err)
			}
			return
		}

		vars := r.URL.Query()
		keyVar := vars.Get("key")

		if keyVar == "" {
			w.WriteHeader(http.StatusBadRequest)
			if err := json.NewEncoder(w).Encode(map[string]any{"code": http.StatusBadRequest, "detail": "Key value not specified"}); err != nil {
				log.Printf("Error encoding response: %s", err)
			}
			return
		}
		worker.respond(r.Context(), keyVar, r, w)
	})
}

func (worker *ReadAPIWorker) lockKey(ctx context.Context, key string) (bool, error) {
	set, err := worker.redisClient.SetNX(
		ctx,
		key+".counter-lock",
		worker.ID,
		time.Duration(worker.config.RedisCacheTTLms)*time.Millisecond,
	).Result()
	if err != nil {
		log.Printf("Error querying redis server: %s", err)
		return false, fmt.Errorf("error locking key: %w", err)
	}

	return set, nil
}

func (worker *ReadAPIWorker) unlockKey(ctx context.Context, key string) (bool, error) {
	// unlock if the worker's ID matches the lock
	const query = `
		local key = redis.call('GET', KEYS[1])
		if key == ARGV[1] then
			redis.call('DEL', KEYS[1])
			return 1
		else
			return 0
		end
	`
	redisScript := redis.NewScript(query)
	result, err := redisScript.Run(
		ctx, worker.redisClient, []string{key + ".counter-lock"}, worker.ID,
	).Result()
	if err != nil {
		log.Printf("Error querying redis: %s", err)
		return false, fmt.Errorf("error unlocking key: %w", err)
	}

	return result.(int64) == 1, nil
}

func (worker *ReadAPIWorker) setCache(ctx context.Context, key string) (int64, error) {
	var val int64
	err := worker.dbsession.Query(`SELECT counter FROM counters WHERE key = ?`, key).WithContext(ctx).Scan(&val)
	if err != nil {
		log.Printf("Error querying redis cache: %s", err)
		return 0, fmt.Errorf("error querying redis cache: %w", err)
	}
	// set cache value
	err = worker.redisClient.Set(ctx, key, val, time.Duration(worker.config.RedisCacheTTLms)*time.Millisecond).Err()
	if err != nil {
		log.Printf("Error setting key: %s", err)
		return 0, fmt.Errorf("error setting key: %w", err)
	}
	return val, nil
}

func (worker *ReadAPIWorker) readKey(ctx context.Context, key string) (int64, error) {
	var valStr string

	for {
		var err error
		valStr, err = worker.redisClient.Get(ctx, key).Result()

		if err == nil {
			break
		}
		if err == redis.Nil {
			// if key not exist, populate the cache
			// first lock the cache to prevent stampede
			locked, err := worker.lockKey(ctx, key)
			if err != nil {
				log.Printf("Error querying redis cache: %s", err)
				return 0, fmt.Errorf("error locking key: %w", err)
			}
			if locked {
				// if locked, query database and populate cache
				val, err := worker.setCache(ctx, key)
				if err != nil {
					log.Printf("Error querying redis cache: %s", err)

					if _, err := worker.unlockKey(ctx, key); err != nil {
						log.Printf("Error deleting key: %s", err)
					}
					return 0, fmt.Errorf("error querying redis cache: %w", err)
				}
				if _, err := worker.unlockKey(ctx, key); err != nil {
					log.Printf("Error deleting key: %s", err)
				}
				return val, nil
			} else {
				time.Sleep(10 * time.Millisecond)
				continue
			}
		}
		return 0, fmt.Errorf("error reading key: %w", err)
	}

	val, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("error parsing value: %w, getting %s", err, valStr)
	}

	return val, nil
}

// directly send the result as JSON
func (worker *ReadAPIWorker) respond(ctx context.Context, keyVar string, r *http.Request, w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")

	val, err := worker.readKey(ctx, keyVar)
	if err != nil {
		log.Printf("Error sending response: %s", err)

		w.WriteHeader(http.StatusInternalServerError)
		if err := json.NewEncoder(w).Encode(map[string]any{"code": http.StatusInternalServerError, "detail": "Error reading key"}); err != nil {
			log.Printf("Error encoding response: %s", err)
		}
		return
	}
	if err := json.NewEncoder(w).Encode(CounterResponse{Key: keyVar, Value: val}); err != nil {
		log.Printf("Error encoding response: %s", err)
	}
}
