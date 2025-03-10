package config

import (
	"github.com/anycable/anycable-go/metrics"
	"github.com/anycable/anycable-go/node"
	"github.com/anycable/anycable-go/pubsub"
	"github.com/anycable/anycable-go/rpc"
	"github.com/anycable/anycable-go/server"
)

// Config contains main application configuration
type Config struct {
	App                  node.Config
	RPC                  rpc.Config
	Redis                pubsub.RedisConfig
	HTTPPubSub           pubsub.HTTPConfig
	Host                 string
	Port                 int
	BroadcastAdapter     string
	Path                 string
	HealthPath           string
	Headers              []string
	SSL                  server.SSLConfig
	WS                   node.WSConfig
	MaxMessageSize       int64
	DisconnectorDisabled bool
	DisconnectQueue      node.DisconnectQueueConfig
	LogLevel             string
	LogFormat            string
	Metrics              metrics.Config
}

// New returns a new empty config
func New() Config {
	config := Config{}
	config.App = node.NewConfig()
	config.SSL = server.NewSSLConfig()
	config.WS = node.NewWSConfig()
	config.Metrics = metrics.NewConfig()
	config.RPC = rpc.NewConfig()
	config.Redis = pubsub.NewRedisConfig()
	config.HTTPPubSub = pubsub.NewHTTPConfig()
	config.DisconnectQueue = node.NewDisconnectQueueConfig()
	return config
}
