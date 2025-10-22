package api

import (
	"encoding/binary"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

type Event struct {
	Id         string    // universal ID for the event
	Timestamp  time.Time // timestamp
	EventKey   string    // event key for counter aggregation
	EventValue uint32
}

type WriteAPIWorkerConfig struct {
	KafkaAddr string
	Topic     string
}

// WRite API worker receives event and
type WriteAPIWorker struct {
	mux      *http.ServeMux
	producer *kafka.Producer
}

func NewWriteAPIWorker(config WriteAPIWorkerConfig) (*WriteAPIWorker, error) {
	// create producer for sending kafka requests
	producer, err := kafka.NewProducer(&kafka.ConfigMap{"bootstrap.servers": config.KafkaAddr})
	if err != nil {
		return nil, err
	}

	// create mux for handling HTTP req
	mux := http.NewServeMux()
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		var event Event
		defer r.Body.Close()

		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			w.WriteHeader(http.StatusBadRequest)

			json.NewEncoder(w).Encode(map[string]any{"code": http.StatusBadRequest, "detail": "Error parsing input request"})
			return
		}

		valueBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(valueBytes, uint32(event.EventValue))

		// send a write event to kafka
		producer.Produce(&kafka.Message{
			TopicPartition: kafka.TopicPartition{
				Topic:     &config.Topic,
				Partition: kafka.PartitionAny,
			},
			Key:   []byte(event.EventKey),
			Value: valueBytes,
		}, nil)

		w.WriteHeader(http.StatusAccepted)
	})

	return &WriteAPIWorker{
		producer: producer,
		mux:      mux,
	}, nil
}

func (w *WriteAPIWorker) Serve(addr string) error {
	log.Printf("Starting write API workers...")

	return http.ListenAndServe(addr, w.mux)
}
