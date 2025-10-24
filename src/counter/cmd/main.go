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
	Long:  `A Kafka-based counter application that can runs different stages of counter worker.`,
}

var writeAPICmd = &cobra.Command{
	Use:   "write-api",
	Short: "Run the Kafka producer",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWriteAPIWorker()
	},
}

var aggregatorCmd = &cobra.Command{
	Use:   "consumer",
	Short: "Run the Kafka consumer and aggregator",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAggregator()
	},
}
var readAPICmd = &cobra.Command{
	Use:   "read-api",
	Short: "Run the read API worker",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runReadAPIWorker()
	},
}

func init() {
	rootCmd.AddCommand(writeAPICmd)
	rootCmd.AddCommand(aggregatorCmd)
	rootCmd.AddCommand(readAPICmd)
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

func runAggregator() error {
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

func runReadAPIWorker() error {
	config, err := config.LoadDefaultConfig()
	if err != nil {
		return fmt.Errorf("error loading configuration: %w", err)
	}
	readAPIWorker, err := api.NewReadAPIWorker(config)
	if err != nil {
		return fmt.Errorf("error launching read API worker: %w", err)
	}

	return readAPIWorker.Serve(":8081")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatalf("Error: %s\n", err)
		os.Exit(1)
	}
}
