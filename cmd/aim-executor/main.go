package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/aimdotsh/aim/internal/executor"
)

const configPath = "/etc/aim/executor.json"

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "aim-executor accepts no command-line arguments")
		os.Exit(2)
	}
	cfg, err := executor.LoadConfig(configPath)
	if err != nil {
		fatal(err)
	}
	req, err := executor.DecodeRequest(os.Stdin)
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	if err := executor.Execute(ctx, req, cfg, os.Stdout); err != nil {
		os.Exit(1)
	}
}

func fatal(err error) {
	_ = json.NewEncoder(os.Stdout).Encode(executor.Event{
		Protocol: executor.ProtocolVersion,
		Time:     time.Now().UTC(),
		Level:    "error",
		Phase:    "executor",
		Message:  err.Error(),
	})
	os.Exit(2)
}
