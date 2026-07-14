package apiserver

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-logr/logr"
	"github.com/thetechnick/orlop/pkg/apiserver/conversion"
	"k8s.io/apimachinery/pkg/runtime"
)

// Server represents the API server with both private and public endpoints.
type Server struct {
	privateRouter chi.Router
	publicRouter  chi.Router
	privateServer *http.Server
	publicServer  *http.Server
	options       Options
	logger        logr.Logger
}

// Options holds server configuration.
type Options struct {
	Address         string
	PrivatePort     int
	PublicPort      int
	CORSOrigins     []string
	EnablePublicAPI bool
	PrivateRegistry  *ResourceRegistry                  // Optional: pre-built registry for private API (skips PrivateResources/PrivateScheme/StorageFactory)
	PrivateResources []ResourceInfo
	PublicResources  []ResourceInfo
	PrivateScheme    *runtime.Scheme
	PublicScheme     *runtime.Scheme
	PrivatePrefix    string                             // Optional: prefix for private labels/annotations/conditions filtered during conversion (defaults to conversion.DefaultPrivatePrefix)
	PrivateMiddleware []func(http.Handler) http.Handler // Optional: custom middleware applied to the private API server
	PublicMiddleware  []func(http.Handler) http.Handler // Optional: custom middleware applied to the public API server
	StorageFactory    StorageFactory                    // Optional: custom storage factory (defaults to in-memory)
	Logger            logr.Logger                       // Optional: logger for server operations (defaults to discard logger)
}

// New creates a new API server with the given options.
func New(opts Options) (*Server, error) {
	logger := opts.Logger
	if logger.GetSink() == nil {
		logger = logr.Discard()
	}

	var registryOpts []RegistryOption
	if opts.StorageFactory != nil {
		registryOpts = append(registryOpts, WithStorageFactory(opts.StorageFactory))
	}
	registryOpts = append(registryOpts, WithLogger(logger))

	privateRegistry := opts.PrivateRegistry
	if privateRegistry == nil {
		if opts.PrivateScheme == nil {
			return nil, fmt.Errorf("private scheme is required")
		}
		if len(opts.PrivateResources) == 0 {
			return nil, fmt.Errorf("at least one private resource is required")
		}

		privateRegistry = NewResourceRegistry(opts.PrivateScheme, registryOpts...)
		for _, res := range opts.PrivateResources {
			if err := privateRegistry.Register(res); err != nil {
				return nil, fmt.Errorf("failed to register private resource %s: %w", res.Plural, err)
			}
		}
	}

	privateRouter, err := setupRouter(privateRegistry, opts.CORSOrigins, opts.PrivateMiddleware)
	if err != nil {
		return nil, fmt.Errorf("failed to setup private router: %w", err)
	}

	privateServer := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", opts.Address, opts.PrivatePort),
		Handler: privateRouter,
	}

	server := &Server{
		privateRouter: privateRouter,
		privateServer: privateServer,
		options:       opts,
		logger:        logger,
	}

	if opts.EnablePublicAPI {
		if opts.PublicScheme == nil {
			return nil, fmt.Errorf("public scheme is required when EnablePublicAPI is true")
		}
		if len(opts.PublicResources) == 0 {
			return nil, fmt.Errorf("public resources are required when EnablePublicAPI is true")
		}

		publicRegistry := NewResourceRegistry(opts.PublicScheme, registryOpts...)
		for _, res := range opts.PublicResources {
			if err := publicRegistry.Register(res); err != nil {
				return nil, fmt.Errorf("failed to register public resource %s: %w", res.Plural, err)
			}
		}

		converter := conversion.NewConverter(opts.PublicScheme, opts.PrivateScheme, opts.PrivatePrefix)
		publicRouter, err := setupConvertingRouter(publicRegistry, privateRegistry, converter, opts.PrivateScheme, opts.CORSOrigins, opts.PublicMiddleware)
		if err != nil {
			return nil, fmt.Errorf("failed to setup public router: %w", err)
		}

		publicServer := &http.Server{
			Addr:    fmt.Sprintf("%s:%d", opts.Address, opts.PublicPort),
			Handler: publicRouter,
		}

		server.publicRouter = publicRouter
		server.publicServer = publicServer
	}

	return server, nil
}

// Run starts the API server(s).
func (s *Server) Run() error {
	// Start private API server in goroutine
	go func() {
		s.logger.Info("Private API server listening", "addr", s.privateServer.Addr)
		if err := s.privateServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error(err, "Private API server error")
		}
	}()

	// Start public API server if enabled
	if s.options.EnablePublicAPI && s.publicServer != nil {
		s.logger.Info("Public API server listening", "addr", s.publicServer.Addr)
		return s.publicServer.ListenAndServe()
	}

	// If public API is not enabled, block on a channel
	select {}
}

// Shutdown gracefully shuts down the server(s).
func (s *Server) Shutdown(ctx context.Context) error {
	// Shutdown private server
	if err := s.privateServer.Shutdown(ctx); err != nil {
		return err
	}

	// Shutdown public server if enabled
	if s.publicServer != nil {
		if err := s.publicServer.Shutdown(ctx); err != nil {
			return err
		}
	}

	return nil
}

// PrivateAddress returns the private server's listen address.
func (s *Server) PrivateAddress() string {
	return s.privateServer.Addr
}

// PublicAddress returns the public server's listen address.
func (s *Server) PublicAddress() string {
	if s.publicServer != nil {
		return s.publicServer.Addr
	}
	return ""
}
