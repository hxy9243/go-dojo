package api

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"

	"github.com/hxy9243/go-dojo/counter/pkg/config"
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

// Write API worker receives event and
type WriteAPIWorker struct {
	mux      *http.ServeMux
	producer *kafka.Producer
}

func initKafka(config config.Config) error {
	// Create an AdminClient instance
	configMap := &kafka.ConfigMap{
		"bootstrap.servers": config.KafkaAddr,
	}
	adminClient, err := kafka.NewAdminClient(configMap)
	if err != nil {
		fmt.Printf("Failed to create Admin client: %s\n", err)
		return fmt.Errorf("error creating admin client: %w", err)
	}
	defer adminClient.Close()

	// Define the new topic
	topicName := config.KafkaTopic
	numPartitions := 16
	replicationFactor := 1

	newTopic := kafka.TopicSpecification{
		Topic:             topicName,
		NumPartitions:     numPartitions,
		ReplicationFactor: replicationFactor,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	results, err := adminClient.CreateTopics(ctx, []kafka.TopicSpecification{newTopic}, kafka.SetAdminOperationTimeout(5*time.Second))
	if err != nil {
		fmt.Printf("Failed to create topic: %s\n", err)
		return fmt.Errorf("error creating topic: %w", err)
	}

	// Process the results
	for _, result := range results {
		if result.Error.Code() != kafka.ErrNoError && result.Error.Code() != kafka.ErrTopicAlreadyExists {
			log.Printf("Failed to create topic %s: %s\n", result.Topic, result.Error)

			return fmt.Errorf("error creating topic: %w", result.Error)
		} else if result.Error.Code() == kafka.ErrTopicAlreadyExists {
			log.Printf("Topic %s already exists.\n", result.Topic)
		} else {
			log.Printf("Topic %s created successfully.\n", result.Topic)
		}
	}
	return nil
}

// NewWriteAPIWorker creates a new write API worker
func NewWriteAPIWorker(config config.Config) (*WriteAPIWorker, error) {
	if err := initKafka(config); err != nil {
		return nil, fmt.Errorf("error initializing kafka: %w", err)
	}
	log.Printf("Kafka initialized")

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
			if err := json.NewEncoder(w).Encode(map[string]any{"code": http.StatusMethodNotAllowed, "detail": "Method not allowed"}); err != nil {
				log.Printf("Error encoding response: %s", err)
			}
			return
		}

		var event EventRequest
		defer r.Body.Close()

		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			if err := json.NewEncoder(w).Encode(map[string]any{"code": http.StatusBadRequest, "detail": "Error parsing input request"}); err != nil {
				log.Printf("Error encoding response: %s", err)
			}
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
			if err := json.NewEncoder(w).Encode(map[string]any{"code": http.StatusInternalServerError, "detail": "Error writing to Kafka"}); err != nil {
				log.Printf("Error encoding response: %s", err)
			}
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
