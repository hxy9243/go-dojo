package config

import (
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	KafkaAddr         string   `mapstructure:"KAFKA_ADDR"`
	KafkaTopic        string   `mapstructure:"KAFKA_TOPIC"`
	KafkaGroupID      string   `mapstructure:"KAFKA_GROUP_ID"`
	CassandraAddr     []string `mapstructure:"CASSANDRA_ADDR"`
	CassandraKeyspace string   `mapstructure:"CASSANDRA_KEYSPACE"`
}

func LoadDefaultConfig() (Config, error) {
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// Set default values
	viper.SetDefault("KAFKA_ADDR", "localhost:9092")
	viper.SetDefault("KAFKA_TOPIC", "counter")
	viper.SetDefault("KAFKA_GROUP_ID", "counter-group")
	viper.SetDefault("CASSANDRA_ADDR", "localhost:9042")
	viper.SetDefault("CASSANDRA_KEYSPACE", "counter")

	var config Config
	// Need to manually handle comma-separated string for Cassandra hosts
	cassandraAddrStr := viper.GetString("CASSANDRA_ADDR")
	config.CassandraAddr = strings.Split(cassandraAddrStr, ",")

	// Unmarshal the rest of the config
	if err := viper.Unmarshal(&config); err != nil {
		return config, err
	}

	return config, nil
}
