package aggregate

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/gocql/gocql"

	"github.com/hxy9243/go-dojo/counter/pkg/config"
)

const (
	PartitionTable = "partitions"

	CounterTable = "counters"
)

type PartitionState struct {
	PartitionID int32
	Offset      int64
	Counters    map[string]int64
}

func (ps *PartitionState) Flush(topic string, session *gocql.Session, consumer *kafka.Consumer) error {
	if len(ps.Counters) == 0 {
		return nil
	}

	// flushes to DB
	batch := session.NewBatch(gocql.UnloggedBatch)
	for key, delta := range ps.Counters {
		batch.Query(`UPDATE counters SET counter = counter + ? WHERE key = ?`, delta, key)
	}
	err := session.ExecuteBatch(batch)
	if err != nil {
		return fmt.Errorf("error flushing to DB: %w", err)
	}
	if err := session.Query(`UPDATE partitions SET offset = ? WHERE partition = ?`,
		ps.Offset, ps.PartitionID).Exec(); err != nil {
		return fmt.Errorf("error flushing to DB: %w", err)
	}

	ps.Counters = make(map[string]int64)

	// commit to kafka
	if _, err := consumer.CommitOffsets([]kafka.TopicPartition{
		{
			Topic:     &topic,
			Partition: ps.PartitionID,
			Offset:    kafka.Offset(ps.Offset),
		},
	}); err != nil {
		return fmt.Errorf("error committing to Kafka: %w", err)
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
	config config.Config

	consumer  *kafka.Consumer
	dbsession *gocql.Session

	partitions map[int32]*PartitionState
}

// InitCassandra initializes the cassandra keyspace and tables
func initCassandra(config config.Config) error {
	// create cassandra connection to system keyspace
	cluster := gocql.NewCluster(config.CassandraAddr...)
	cluster.Keyspace = "system"
	cluster.Consistency = gocql.Quorum
	session, err := cluster.CreateSession()
	if err != nil {
		return fmt.Errorf("error connecting to cassandra: %w", err)
	}
	defer session.Close()

	// create keyspace
	if err := session.Query(
		fmt.Sprintf(`CREATE KEYSPACE IF NOT EXISTS %s WITH REPLICATION = { 'class' : 'SimpleStrategy', 'replication_factor' : %d };`,
			config.CassandraKeyspace,
			config.CassandraReplication,
		)).Exec(); err != nil {
		return fmt.Errorf("error creating keyspace: %w", err)
	}
	log.Printf("Keyspace %s created or already exists", config.CassandraKeyspace)

	// connect to keyspace to create tables
	cluster.Keyspace = config.CassandraKeyspace
	ksSession, err := cluster.CreateSession()
	if err != nil {
		return fmt.Errorf("error connecting to cassandra keyspace %s: %w", config.CassandraKeyspace, err)
	}
	defer ksSession.Close()

	// create partitions table
	err = ksSession.Query(`CREATE TABLE IF NOT EXISTS partitions (partition int PRIMARY KEY, offset bigint);`).Exec()
	if err != nil {
		return fmt.Errorf("error creating partitions table: %w", err)
	}
	log.Printf("Table partitions created or already exists")

	// create counters table
	err = ksSession.Query(`CREATE TABLE IF NOT EXISTS counters (key text PRIMARY KEY, counter counter);`).Exec()
	if err != nil {
		return fmt.Errorf("error creating counters table: %w", err)
	}
	log.Printf("Table counters created or already exists")

	log.Printf("Cassandra keyspace and tables initialized successfully")
	return nil
}

func newAggregator(config config.Config) (*Aggregator, error) {
	// init aggregator
	if err := initCassandra(config); err != nil {
		return nil, fmt.Errorf("error initializing cassandra: %w", err)
	}

	// create kafka connection
	consumer, err := kafka.NewConsumer(&kafka.ConfigMap{
		"bootstrap.servers":               config.KafkaAddr,
		"group.id":                        config.KafkaGroupID,
		"go.application.rebalance.enable": true,
		"enable.auto.commit":              false,
		"auto.offset.reset":               "earliest",
		// "max.poll.interval.ms":            500,
		// "session.timeout.ms":              20,
	})
	if err != nil {
		return nil, fmt.Errorf("error creating kafka consumer: %w", err)
	}
	if err := consumer.SubscribeTopics([]string{config.KafkaTopic}, nil); err != nil {
		return nil, fmt.Errorf("error subscribing kafka topic: %w", err)
	}
	log.Printf("Connected to Kafka at %s, subscribed to topic %s, group %s", config.KafkaAddr, config.KafkaTopic, config.KafkaGroupID)

	// create cassandra connection
	cluster := gocql.NewCluster(config.CassandraAddr...)
	cluster.Keyspace = config.CassandraKeyspace
	cluster.Consistency = gocql.Quorum
	dbsession, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("error connecting to cassandra: %w", err)
	}
	log.Printf("Connected to cassandra %s at keyspace %s", config.CassandraAddr, config.CassandraKeyspace)

	return &Aggregator{
		config: config,

		consumer:  consumer,
		dbsession: dbsession,

		partitions: make(map[int32]*PartitionState),
	}, nil
}

// NewAggregator creates an aggregator with backoff
func NewAggregator(config config.Config) (*Aggregator, error) {
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

	return nil, fmt.Errorf("error initializing aggregator after %d tries", Retries)
}

func (agg *Aggregator) lookupPartition(partition int32) (*PartitionState, error) {
	po, ok := agg.partitions[partition]
	if ok {
		return po, nil
	}

	var (
		offset int64
	)
	err := agg.dbsession.Query(
		`SELECT offset FROM `+PartitionTable+` WHERE partition = ?`, partition,
	).Scan(&offset)

	if err != nil {
		if err == gocql.ErrNotFound {
			po = &PartitionState{PartitionID: partition, Offset: 0, Counters: make(map[string]int64)}
		} else {
			return nil, fmt.Errorf("error querying database: %w", err)
		}
	} else {
		po = &PartitionState{
			PartitionID: partition,
			Offset:      offset,
			Counters:    make(map[string]int64),
		}
	}
	agg.partitions[partition] = po
	return po, nil
}

func (agg *Aggregator) processMessage(msg *kafka.Message) error {
	if len(msg.Value) != 4 {
		return fmt.Errorf("error invalid counter message: %s", string(msg.Value))
	}

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
		return fmt.Errorf("error looking up partition: %w", err)
	}
	if offset <= partitionCount.Offset {
		return nil
	}

	partitionCount.Offset = offset + 1

	existVal, ok := partitionCount.Counters[key]
	if !ok {
		partitionCount.Counters[key] = int64(val)
	} else {
		partitionCount.Counters[key] = existVal + int64(val)
	}

	log.Printf("Counter %s at %d", key, partitionCount.Counters[key])

	return nil
}

func (agg *Aggregator) flushAll() error {
	errChan := make(chan error, len(agg.partitions))
	wg := &sync.WaitGroup{}

	for _, partition := range agg.partitions {
		wg.Go(func() {
			if err := partition.Flush(agg.config.KafkaTopic, agg.dbsession, agg.consumer); err != nil {
				errChan <- err
			}
		})
	}

	go func() {
		wg.Wait()
		close(errChan)
	}()

	var errs error
	for err := range errChan {
		errs = errors.Join(errs, err)
	}
	if errs != nil {
		return fmt.Errorf("error flushing all records, encountered errors: %w", errs)
	}
	return nil
}

/*
Aggregator runloop is the main entry for the aggregator. It handles:
- Checks after start or recovery
- Main runloop and aggregation, it performs:
  - Reads from Kafaka stream
  - Aggregate based on message key value
  - Flushes to database periodically

- Handles rebalance when partitions leave consumer worker
*/
func (agg *Aggregator) run() error {
	log.Printf("Starting aggregate worker runloop...")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	run := true
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for run {
		select {
		case <-sigChan:
			log.Printf("Signal caught, quitting...")
			run = false

			if err := agg.flushAll(); err != nil {
				log.Printf("Error flushing aggregate")
			}

		case <-ticker.C:
			if err := agg.flushAll(); err != nil {
				log.Printf("Error flushing aggregate: %s", err)
			}
		default:
			evt := agg.consumer.Poll(100)
			if evt == nil {
				continue
			}

			switch e := evt.(type) {
			case *kafka.Message:
				// handles regular message
				if err := agg.processMessage(e); err != nil {
					log.Printf("Error processing message: %s", err)
				}
			case *kafka.RevokedPartitions:
				// handles partition rebalance
				log.Printf("Repartitioning, flushing leaving partitions...")

				for _, partition := range e.Partitions {
					partitionCounter, ok := agg.partitions[partition.Partition]
					if !ok {
						continue
					}
					log.Printf("Repartitioning ID %d", partitionCounter.PartitionID)

					if err := partitionCounter.Flush(agg.config.KafkaTopic, agg.dbsession, agg.consumer); err != nil {
						log.Printf("Error flushing partition %d during rebalance: %s", partitionCounter.PartitionID, err)
					}
				}

				for _, partition := range e.Partitions {
					delete(agg.partitions, partition.Partition)
				}
			case kafka.Error:
				log.Printf("Error encountered when reading from Kafka: %s", e)
				return fmt.Errorf("error reading from Kafka: %w", e)

			default:
				log.Printf("Unknown event: %s, %T", e, e)
			}
		}
	}

	return nil
}

func (agg *Aggregator) Run() error {
	log.Printf("Starting aggregator...")

	return agg.run()
}

func (agg *Aggregator) Close() error {
	agg.dbsession.Close()

	return agg.consumer.Close()
}
