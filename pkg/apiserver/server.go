package apiserver

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/thetechnick/orlop/pkg/apiserver/conversion"
	"github.com/thetechnick/orlop/pkg/apiserver/storage"
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
	Address        string
	PrivatePort    int
	PublicPort     int
	CORSOrigins    []string
	EnablePublicAPI bool
}

// New creates a new API server with the given options.
func New(opts Options) (*Server, error) {
	// Create private API
	privateRegistry := NewResourceRegistry()
	RegisterTestResources(privateRegistry)

	// Create per-type stores for private API
	privateStores := make(map[string]storage.ResourceStore)
	for _, res := range privateRegistry.GetResources() {
		privateStores[res.Plural] = storage.NewMemoryStore(res.Plural)
	}

	privateRouter, err := setupRouter(privateStores, privateRegistry, opts.CORSOrigins)
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
		publicRegistry := NewResourceRegistry()
		RegisterPublicResources(publicRegistry)

		// Create per-type stores for public API (shared with private stores for same types)
		publicStores := make(map[string]storage.ResourceStore)
		for _, res := range publicRegistry.GetResources() {
			// Reuse private store if it exists for the same resource type
			if privateStore, ok := privateStores[res.Plural]; ok {
				publicStores[res.Plural] = privateStore
			} else {
				publicStores[res.Plural] = storage.NewMemoryStore(res.Plural)
			}
		}

		converter := conversion.NewConverter()
		publicRouter, err := setupConvertingRouter(publicStores, publicRegistry, converter, opts.CORSOrigins)
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
