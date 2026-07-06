package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/thetechnick/orlop/pkg/apiserver"
)

func main() {
	var (
		address     string
		port        int
		corsOrigins string
	)

	flag.StringVar(&address, "address", "0.0.0.0", "address to bind to")
	flag.IntVar(&port, "port", 8080, "port to listen on")
	flag.StringVar(&corsOrigins, "cors-origins", "*", "comma-separated list of allowed CORS origins")
	flag.Parse()

	// Parse CORS origins
	origins := []string{}
	if corsOrigins != "" {
		origins = strings.Split(corsOrigins, ",")
		for i, origin := range origins {
			origins[i] = strings.TrimSpace(origin)
		}
	}

	// Create server
	opts := apiserver.Options{
		Address:     address,
		Port:        port,
		CORSOrigins: origins,
	}

	server, err := apiserver.New(opts)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start server in goroutine
	go func() {
		log.Printf("Starting orlop-server on %s", server.Address())
		if err := server.Run(); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for signal
	<-sigChan
	log.Println("Shutting down server...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server shutdown error: %v", err)
	}

	log.Println("Server stopped")
}
