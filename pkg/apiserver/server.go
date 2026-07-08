package apiserver

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
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
}

// Options holds server configuration.
type Options struct {
	Address          string
	PrivatePort      int
	PublicPort       int
	CORSOrigins      []string
	EnablePublicAPI  bool
	PrivateResources []ResourceInfo
	PublicResources  []ResourceInfo
	PrivateScheme    *runtime.Scheme
	PublicScheme     *runtime.Scheme
}

// New creates a new API server with the given options.
func New(opts Options) (*Server, error) {
	// Validate options
	if opts.PrivateScheme == nil {
		return nil, fmt.Errorf("private scheme is required")
	}
	if len(opts.PrivateResources) == 0 {
		return nil, fmt.Errorf("at least one private resource is required")
	}

	// Create private API registry with scheme
	// Registry will create stores for each registered resource
	privateRegistry := NewResourceRegistry(opts.PrivateScheme)
	for _, res := range opts.PrivateResources {
		privateRegistry.Register(res)
	}

	privateRouter, err := setupRouter(privateRegistry, opts.CORSOrigins)
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
	}

	// Create public API if enabled
	if opts.EnablePublicAPI {
		if opts.PublicScheme == nil {
			return nil, fmt.Errorf("public scheme is required when EnablePublicAPI is true")
		}
		if len(opts.PublicResources) == 0 {
			return nil, fmt.Errorf("public resources are required when EnablePublicAPI is true")
		}

		// Public API uses separate scheme and registry
		publicRegistry := NewResourceRegistry(opts.PublicScheme)
		for _, res := range opts.PublicResources {
			publicRegistry.Register(res)
		}

		converter := conversion.NewConverter()
		publicRouter, err := setupConvertingRouter(publicRegistry, converter, opts.PrivateScheme, opts.CORSOrigins)
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
		log.Printf("Private API server listening on %s", s.privateServer.Addr)
		if err := s.privateServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Private API server error: %v", err)
		}
	}()

	// Start public API server if enabled
	if s.options.EnablePublicAPI && s.publicServer != nil {
		log.Printf("Public API server listening on %s", s.publicServer.Addr)
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
