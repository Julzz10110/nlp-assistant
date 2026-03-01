package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type ServiceConfig struct {
	Name string `yaml:"name"`
	Port int    `yaml:"port"`
}

type RedisConfig struct {
	Address  string        `yaml:"address"`
	Password string        `yaml:"password"`
	TTL      time.Duration `yaml:"ttl"`
}

type GRPCServiceClientConfig struct {
	Address string        `yaml:"address"`
	Timeout time.Duration `yaml:"timeout"`
	Retries int           `yaml:"retries"`
}

type OTELConfig struct {
	Exporter       string  `yaml:"exporter"`
	JaegerEndpoint string  `yaml:"jaeger_endpoint"`
	SampleRate     float64 `yaml:"sample_rate"`
}

type OrchestratorConfig struct {
	Service ServiceConfig `yaml:"service"`
	Redis   RedisConfig   `yaml:"redis"`

	Services struct {
		Classifier GRPCServiceClientConfig `yaml:"classifier"`
		Extractor  GRPCServiceClientConfig `yaml:"extractor"`
		Weather    GRPCServiceClientConfig `yaml:"weather"`
		Reminder   GRPCServiceClientConfig `yaml:"reminder"`
	} `yaml:"services"`

	OTEL OTELConfig `yaml:"otel"`
}

func LoadOrchestratorConfig(path string) (*OrchestratorConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg OrchestratorConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

