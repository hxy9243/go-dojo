package aggregate

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/gocql/gocql"
)

const (
	PartitionTable = "partitions"

	CounterTable = "counters"
)

type AggregatorConfig struct {
	KafkaAddr string
	Topic     string
	GroupId   string

	CassandraAddr     []string
	CassandraKeyspace string
}

type PartitionState struct {
	PartitionID int32
	Offset      int64
	Counters    map[string]int64
}

func (ps *PartitionState) Flush(session *gocql.Session, consumer *kafka.Consumer) error {
	if len(ps.Counters) == 0 {
		return nil
	}

	// flushes to DB
	batch := session.NewBatch(gocql.UnloggedBatch)
	for key, delta := range ps.Counters {
		batch.Query(`UPDATE counters SET total = total + ? WHERE key = ?`, delta, key)
	}
	batch.Query(`UPDATE partitions SET offset = ? WHERE partition_id = ?`,
		ps.Offset, ps.PartitionID)

	err := session.ExecuteBatch(batch)
	if err != nil {
		return fmt.Errorf("Error flushing to DB: %s", err)
	}
	ps.Counters = make(map[string]int64)

	// commit to kafka
	if _, err := consumer.CommitOffsets([]kafka.TopicPartition{
		{Partition: ps.PartitionID, Offset: kafka.Offset(ps.Offset)},
	}); err != nil {
		return fmt.Errorf("Error commiting to Kafka: %s", err)
	}

	return nil
}

/*
Aggregator Worker

# Overview

The aggregator worker performs the following in the run loop:

- Reads from Kafaka stream
- Aggregate based on message key value
- Flushes to database periodically

# Expect cassandra tables:

table partition
partition int primary key
offset in64

table counter
key string primary key
count Counter
*/
type Aggregator struct {
	config AggregatorConfig

	consumer  *kafka.Consumer
	dbsession *gocql.Session

	partitions map[int32]PartitionState
}

func newAggregator(config AggregatorConfig) (*Aggregator, error) {
	// create kafka connection
	consumer, err := kafka.NewConsumer(&kafka.ConfigMap{
		"bootstrap.servers":               config.KafkaAddr,
		"group.id":                        config.GroupId,
		"go.application.rebalance.enable": true,
		"auto.offset.reset":               "manual",
		// "max.poll.interval.ms": 1000,
		// "session.timeout.ms":   500,
	})
	if err != nil {
		return nil, fmt.Errorf("Error creating kafka consumer: %s", err)
	}
	if err := consumer.SubscribeTopics([]string{}, nil); err != nil {
		return nil, fmt.Errorf("Error subscribing kafka topic: %s", err)
	}

	// create cassandra connection
	cluster := gocql.NewCluster(config.CassandraAddr...)
	cluster.Keyspace = config.CassandraKeyspace
	cluster.Consistency = gocql.Quorum
	dbsession, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("Error connecting to cassandra: %s", err)
	}

	return &Aggregator{
		config: config,

		consumer:  consumer,
		dbsession: dbsession,

		partitions: make(map[int32]PartitionState),
	}, nil
}

// NewAggregator creates an aggregator with backoff
func NewAggregator(config AggregatorConfig) (*Aggregator, error) {
	const Retries = 5

	for range Retries {
		agg, err := newAggregator(config)
		if err != nil {
			log.Printf("Warn: error creating aggregator instance: %s, waiting...", err)

			time.Sleep(5 * time.Second)
			continue
		}

		return agg, nil
	}

	return nil, fmt.Errorf("Error initializing aggregator after %d tries", Retries)
}

func (agg *Aggregator) lookupPartition(partition int32) (PartitionState, error) {
	po, ok := agg.partitions[partition]
	if ok {
		return po, nil
	}

	var (
		offset int64
	)
	err := agg.dbsession.Query(
		`SELECT offset FROM `+PartitionTable+` WHERE KEY = ? LIMIT 1`,
		partition,
	).Scan(&offset)

	if err != nil {
		if err == gocql.ErrNotFound {
			// use invalid partition to indicate not found
			return PartitionState{PartitionID: partition, Offset: 0, Counters: make(map[string]int64)}, nil
		}
		return PartitionState{}, fmt.Errorf("Error querying database: %s", err)
	}

	po = PartitionState{
		PartitionID: partition,
		Offset:      offset,
		Counters:    make(map[string]int64),
	}
	agg.partitions[partition] = po
	return po, nil
}

func (agg *Aggregator) processMessage(msg *kafka.Message) error {
	var (
		key string = string(msg.Key)
		val uint32 = binary.BigEndian.Uint32(msg.Value)

		partition int32 = msg.TopicPartition.Partition
		offset    int64 = int64(msg.TopicPartition.Offset)
	)

	log.Printf("Processing message (part %d: %d) %s: %d", partition, offset, key, val)

	// check for partition offset, skip if offset
	partitionCount, err := agg.lookupPartition(partition)
	if err != nil {
		return err
	}
	if offset <= partitionCount.Offset {
		return nil
	}

	partitionCount.Offset = offset

	existVal, ok := partitionCount.Counters[key]
	if !ok {
		partitionCount.Counters[key] = 0
	} else {
		partitionCount.Counters[key] = existVal + int64(val)
	}
	return nil
}

func (agg *Aggregator) flushAll() error {
	errs := []string{}

	for _, partition := range agg.partitions {
		if err := partition.Flush(agg.dbsession, agg.consumer); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("Error flushing to DB, encountered errors: %s", strings.Join(errs, "; "))
}

func (agg *Aggregator) run() error {
	log.Printf("Starting aggregate worker runloop...")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	run := true
	for run {
		select {
		case <-sigChan:
			log.Printf("Signal caught, quitting...")
			run = false

			if err := agg.flushAll(); err != nil {
				log.Printf("Error flushing aggregate")
			}
		default:
			evt := agg.consumer.Poll(1000)
			if evt == nil {
				continue
			}

			switch e := evt.(type) {
			case *kafka.Message:
				if err := agg.processMessage(e); err != nil {
					log.Printf("Error processing message: %s", err)
				}
			case *kafka.RevokedPartitions:
				log.Printf("Repartitioning, flushing leaving partitions...")

				for _, partition := range e.Partitions {
					partitionCounter, ok := agg.partitions[partition.Partition]
					if !ok {
						continue
					}
					log.Printf("Repartitioning ID %d", partitionCounter.PartitionID)

					if err := partitionCounter.Flush(agg.dbsession, agg.consumer); err != nil {
						log.Printf("Error flushing partition %d during rebalance: %s", partitionCounter.PartitionID, err)
					}
				}

				for _, partition := range e.Partitions {
					delete(agg.partitions, partition.Partition)
				}
			}
		}
	}

	return nil
}

func (agg *Aggregator) Run() error {
	log.Printf("Starting aggregator...")

	/* TODO:
	- Startup sequence checks the partition offset from persistent datastore, and discards msg before offset

	- Run sequence performs the following:
	  - keep reading from kafka
	  - aggregates to different keys
	  - flushes to persistent store once a threshold is reached (key number / timeout)
	*/

	return agg.run()
}

func (agg *Aggregator) Close() error {
	return agg.consumer.Close()
}
