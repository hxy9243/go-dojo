package config

import (
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	KafkaAddr    string `mapstructure:"KAFKA_ADDR"`
	KafkaTopic   string `mapstructure:"KAFKA_TOPIC"`
	KafkaGroupID string `mapstructure:"KAFKA_GROUP_ID"`

	CassandraAddr     []string `mapstructure:"CASSANDRA_ADDR"`
	CassandraKeyspace string   `mapstructure:"CASSANDRA_KEYSPACE"`

	RedisServiceType string   `mapstructure:"REDIS_SERVICE_TYPE"`
	RedisMasterName  string   `mapstructure:"REDIS_MASTER_NAME"`
	RedisAddr        []string `mapstructure:"REDIS_ADDR"`
	RedisPassword    string   `mapstructure:"REDIS_PASSWORD"`

	RedisCacheTTLms int64 `mapstructure:"REDIS_CACHE_TTL_MS"`
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

	viper.SetDefault("REDIS_SERVICE_TYPE", "sentinel")
	viper.SetDefault("REDIS_MASTER_NAME", "mymaster")
	viper.SetDefault("REDIS_ADDR", "localhost:6379")
	viper.SetDefault("REDIS_PASSWORD", "")

	viper.SetDefault("REDIS_CACHE_TTL_MS", 500)

	var config Config
	// Need to manually handle comma-separated string
	cassandraAddrStr := viper.GetString("CASSANDRA_ADDR")
	config.CassandraAddr = strings.Split(cassandraAddrStr, ",")

	redisAddrStr := viper.GetString("REDIS_ADDR")
	config.RedisAddr = strings.Split(redisAddrStr, ",")

	// Unmarshal the rest of the config
	if err := viper.Unmarshal(&config); err != nil {
		return config, err
	}

	return config, nil
}
