package api

import (
	"encoding/binary"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"

	"github.com/hxy9243/go-dojo/counter/config"
)

type EventRequest struct {
	Id         string    `json:"id"`        // universal ID for the event
	Timestamp  time.Time `json:"timestamp"` // timestamp
	EventKey   string    `json:"eventkey"`  // event key for counter aggregation
	EventValue uint32    `json:"eventvalue"`
}

type WriteAPIWorkerConfig struct {
	KafkaAddr  string
	KafkaTopic string
}

// WRite API worker receives event and
type WriteAPIWorker struct {
	mux      *http.ServeMux
	producer *kafka.Producer
}

func NewWriteAPIWorker(config config.Config) (*WriteAPIWorker, error) {
	// create producer for sending kafka requests
	producer, err := kafka.NewProducer(&kafka.ConfigMap{"bootstrap.servers": config.KafkaAddr})
	if err != nil {
		return nil, err
	}
	log.Printf("Connected to Kafka at %s, writing to topic %s", config.KafkaAddr, config.KafkaTopic)

	// create mux for handling HTTP req
	mux := http.NewServeMux()
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]any{"code": http.StatusMethodNotAllowed, "detail": "Method not allowed"})
			return
		}

		var event EventRequest
		defer r.Body.Close()

		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"code": http.StatusBadRequest, "detail": "Error parsing input request"})
			return
		}

		valueBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(valueBytes, uint32(event.EventValue))

		// send a write event to kafka
		if err := producer.Produce(&kafka.Message{
			TopicPartition: kafka.TopicPartition{
				Topic:     &config.KafkaTopic,
				Partition: kafka.PartitionAny,
			},
			Key:   []byte(event.EventKey),
			Value: valueBytes,
		}, nil); err != nil {
			log.Printf("Error writing to kafka for %s: %d", event.EventKey, event.EventValue)
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]any{"code": http.StatusInternalServerError, "detail": "Error writing to Kafka"})
			return
		}

		w.WriteHeader(http.StatusAccepted)
		log.Printf("POST handled event request for %s: %d", event.EventKey, event.EventValue)
	})

	return &WriteAPIWorker{
		producer: producer,
		mux:      mux,
	}, nil
}

func (w *WriteAPIWorker) Serve(addr string) error {
	log.Printf("Starting write API workers, listening to HTTP requests on %s...", addr)

	return http.ListenAndServe(addr, w.mux)
}

func (w *WriteAPIWorker) Close() error {
	w.producer.Close()

	return nil
}
