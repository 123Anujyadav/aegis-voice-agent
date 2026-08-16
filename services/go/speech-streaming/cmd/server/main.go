package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
)

const serviceName = "speech-streaming"
const envPrefix = "CS_SPEECH_STREAMING"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var cfg platform.ServiceConfig
	if err := platform.LoadConfig(&cfg, envPrefix); err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	cfg.Name = serviceName

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate configuration: %w", err)
	}

	hashKey := make([]byte, 32)
	if _, err := rand.Read(hashKey); err != nil {
		return fmt.Errorf("generate log hash key: %w", err)
	}

	logger := platform.NewLogger(cfg, os.Stdout, hashKey)
	platform.SetDefaultLogger(logger)

	health := platform.NewHealth(cfg)
	svc := platform.NewService(cfg, logger, health)

	return svc.Run(context.Background())
}
