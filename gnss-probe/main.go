package main

import (
	"log"
	"os"

	"gnss-probe/internal/config"
	"gnss-probe/internal/probe"
	"gnss-probe/internal/result"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(0)

	cfg, err := config.Load()
	if err != nil {
		log.Printf("[ERROR] Config: %v", err)
		result.Emit(&result.Result{
			Probe:     "gnss",
			Error:     err.Error(),
			ErrorCode: result.ExitConfigError,
		})
		os.Exit(result.ExitConfigError)
	}

	res, exitCode := probe.Run(cfg)
	result.Emit(res)
	os.Exit(exitCode)
}
