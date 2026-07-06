package apiserver

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/thetechnick/orlop/pkg/apiserver/storage"
)

// Server represents the API server.
type Server struct {
	router     chi.Router
	store      storage.ResourceStore
	httpServer *http.Server
	options    Options
}

// Options holds server configuration.
type Options struct {
	Address     string
	Port        int
	CORSOrigins []string
}

// New creates a new API server with the given options.
func New(opts Options) (*Server, error) {
	// Create storage backend
	store := storage.NewMemoryStore()

	// Setup router
	router, err := setupRouter(store, opts.CORSOrigins)
	if err != nil {
		return nil, fmt.Errorf("failed to setup router: %w", err)
	}

	// Create HTTP server
	httpServer := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", opts.Address, opts.Port),
		Handler: router,
	}

	return &Server{
		router:     router,
		store:      store,
		httpServer: httpServer,
		options:    opts,
	}, nil
}

// Run starts the API server.
func (s *Server) Run() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Address returns the server's listen address.
func (s *Server) Address() string {
	return s.httpServer.Addr
}
