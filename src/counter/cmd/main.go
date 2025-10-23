package main

import (
	"fmt"
	"log"
	"os"

	"github.com/hxy9243/go-dojo/counter/aggregate"
	"github.com/hxy9243/go-dojo/counter/api"
	"github.com/spf13/cobra"

	"github.com/hxy9243/go-dojo/counter/config"
)

var rootCmd = &cobra.Command{
	Use:   "counter",
	Short: "A Kafka-based counter application",
	Long:  `A Kafka-based counter application that can run as either a producer or a consumer.`,
}

var producerCmd = &cobra.Command{
	Use:   "write-api",
	Short: "Run the Kafka producer",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWriteAPIWorker()
	},
}

var consumerCmd = &cobra.Command{
	Use:   "consumer",
	Short: "Run the Kafka consumer and aggregator",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConsumer()
	},
}

func init() {
	rootCmd.AddCommand(producerCmd)
	rootCmd.AddCommand(consumerCmd)
}

func runConsumer() error {
	config, err := config.LoadDefaultConfig()
	if err != nil {
		return fmt.Errorf("error loading configuration: %w", err)
	}
	agg, err := aggregate.NewAggregator(config)
	if err != nil {
		return fmt.Errorf("error creating aggregator: %w", err)
	}
	return agg.Run()
}

func runWriteAPIWorker() error {
	config, err := config.LoadDefaultConfig()
	if err != nil {
		return fmt.Errorf("error loading configuration: %w", err)
	}
	writeAPIWorker, err := api.NewWriteAPIWorker(config)
	if err != nil {
		return fmt.Errorf("error launching write API worker: %w", err)
	}

	return writeAPIWorker.Serve(":8080")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatalf("Error: %s\n", err)
		os.Exit(1)
	}
}
