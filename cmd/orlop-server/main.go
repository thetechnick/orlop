package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	"github.com/thetechnick/orlop/pkg/apiserver"
)

func main() {
	var (
		address       string
		privatePort   int
		publicPort    int
		corsOrigins   string
		enablePublic  bool
	)

	flag.StringVar(&address, "address", "0.0.0.0", "address to bind to")
	flag.IntVar(&privatePort, "private-port", 8080, "port for private API")
	flag.IntVar(&publicPort, "public-port", 8081, "port for public API")
	flag.BoolVar(&enablePublic, "enable-public-api", true, "enable public API server")
	flag.StringVar(&corsOrigins, "cors-origins", "*", "comma-separated list of allowed CORS origins")
	flag.Parse()

	// Setup logger using standard library logger backend
	logger := stdr.New(nil)

	// Parse CORS origins
	origins := []string{}
	if corsOrigins != "" {
		origins = strings.Split(corsOrigins, ",")
		for i, origin := range origins {
			origins[i] = strings.TrimSpace(origin)
		}
	}

	// Create server with resource configuration
	opts := apiserver.Options{
		Address:          address,
		PrivatePort:      privatePort,
		PublicPort:       publicPort,
		CORSOrigins:      origins,
		EnablePublicAPI:  enablePublic,
		PrivateResources: getPrivateResources(),
		PublicResources:  getPublicResources(),
		PrivateScheme:    getPrivateScheme(),
		PublicScheme:     getPublicScheme(),
		Logger:           logger,
	}

	server, err := apiserver.New(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create server: %v\n", err)
		os.Exit(1)
	}

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start server in goroutine
	go func() {
		if err := server.Run(); err != nil {
			logger.Error(err, "Server error")
			os.Exit(1)
		}
	}()

	// Wait for signal
	<-sigChan
	logger.Info("Shutting down server")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error(err, "Server shutdown error")
		os.Exit(1)
	}

	logger.Info("Server stopped")
}
