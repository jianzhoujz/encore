package main

import (
	"fmt"
	"os"

	"github.com/jianzhoujz/encore/internal/config"
	"github.com/jianzhoujz/encore/internal/logger"
	"github.com/jianzhoujz/encore/internal/proxy"
)

var version = "0.3.3"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "start":
		runStart()
	case "version":
		fmt.Printf("encore v%s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func runStart() {
	// Load and validate config.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger.
	log, err := logger.New(cfg.Log.ConsoleLevel, cfg.Log.FileLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Close()

	// Start the proxy servers (one per protocol, blocks until error or interrupt).
	if err := proxy.StartServers(cfg, log); err != nil {
		log.Error("Server stopped with error: %s", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Encore - AI API proxy with automatic retry")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  encore <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  start      Start the proxy server")
	fmt.Println("  version    Show version information")
	fmt.Println("  help       Show this help message")
	fmt.Println()
	fmt.Printf("Config: %s/config.json\n", config.ConfigDir())
}
