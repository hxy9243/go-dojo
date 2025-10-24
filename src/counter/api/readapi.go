package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/go-redis/redis/v8"
	"github.com/gocql/gocql"

	"github.com/hxy9243/go-dojo/counter/config"
)

type CounterResponse struct {
	Key   string `json:"key"`
	Value int64  `json:"value"`
}

type ReadAPIWorker struct {
	mux         *http.ServeMux
	redisClient *redis.Client
	dbsession   *gocql.Session
}

func NewReadAPIWorker(config config.Config) (*ReadAPIWorker, error) {
	log.Printf("Starting read API workers...")

	mux := http.NewServeMux()

	var (
		redisClient *redis.Client
		dbsession   *gocql.Session
	)

	redisClient = redis.NewFailoverClient(&redis.FailoverOptions{
		MasterName:       config.RedisMasterName,
		SentinelAddrs:    config.RedisAddr,
		SentinelPassword: config.RedisPassword,
		Password:         config.RedisPassword,
	})

	cluster := gocql.NewCluster(config.CassandraAddr...)
	cluster.Keyspace = config.CassandraKeyspace
	cluster.Consistency = gocql.Quorum
	dbsession, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("error connecting to cassandra: %w", err)
	}

	worker := &ReadAPIWorker{
		redisClient: redisClient,
		mux:         mux,
		dbsession:   dbsession,
	}

	worker.initHandlers()
	return worker, nil
}

func (worker *ReadAPIWorker) Serve(addr string) error {
	log.Printf("Starting serving API...")

	return http.ListenAndServe(addr, worker.mux)
}

func (worker *ReadAPIWorker) Close() error {

	return nil
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

			worker.stream(keyVar, r, w)
			return
		} else {
			log.Printf("HTTP: returning response for key %s", keyVar)

			worker.respond(keyVar, r, w)
		}
	})
}

func (worker *ReadAPIWorker) readKey(key string) (int64, error) {

	return 0, nil
}

func (worker *ReadAPIWorker) watchKey(key string) (chan int64, error) {
	ch := make(chan int64)

	return ch, nil
}

// stream the results as SSE back to client
func (worker *ReadAPIWorker) stream(keyVar string, r *http.Request, w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch, err := worker.watchKey(keyVar)
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
func (worker *ReadAPIWorker) respond(keyVar string, r *http.Request, w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")

	val, err := worker.readKey(keyVar)
	if err != nil {
		log.Printf("Error sending response: %s", err)

		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{"code": http.StatusInternalServerError, "detail": "Error reading key"})
		return
	}
	json.NewEncoder(w).Encode(CounterResponse{Key: keyVar, Value: val})
}
