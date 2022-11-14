package authorization

import (
	// Used to load .env files for environment variables
	_ "github.com/joho/godotenv/autoload"
	envconfig "github.com/kelseyhightower/envconfig"
)

type Config struct {
	Mocked bool   `default:"false"`
	Port   string `default:"8181"`
}

func NewEnvConfig() (*Config, error) {
	var c Config
	err := envconfig.Process("opa", &c)
	return &c, err
}
