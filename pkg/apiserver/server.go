package apiserver

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-logr/logr"
	rbacv1 "github.com/thetechnick/orlop/apis/private/rbac/v1"
	"github.com/thetechnick/orlop/pkg/apiserver/conversion"
	"github.com/thetechnick/orlop/pkg/apiserver/rbac"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	Address          string
	PrivatePort      int
	PublicPort       int
	CORSOrigins      []string
	EnablePublicAPI  bool
	EnableRBAC       bool           // Enable RBAC authorization middleware
	PrivateResources []ResourceInfo
	PublicResources  []ResourceInfo
	PrivateScheme    *runtime.Scheme
	PublicScheme     *runtime.Scheme
	StorageFactory   StorageFactory // Optional: custom storage factory (defaults to in-memory)
	Logger           logr.Logger    // Optional: logger for server operations (defaults to discard logger)
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

	logger := opts.Logger
	if logger.GetSink() == nil {
		// Use a no-op logger if none provided
		logger = logr.Discard()
	}

	// Create private API registry with scheme
	// Registry will create stores for each registered resource using the configured storage factory
	var registryOpts []RegistryOption
	if opts.StorageFactory != nil {
		registryOpts = append(registryOpts, WithStorageFactory(opts.StorageFactory))
	}
	registryOpts = append(registryOpts, WithLogger(logger))

	privateRegistry := NewResourceRegistry(opts.PrivateScheme, registryOpts...)
	for _, res := range opts.PrivateResources {
		if err := privateRegistry.Register(res); err != nil {
			return nil, fmt.Errorf("failed to register private resource %s: %w", res.Plural, err)
		}
	}

	// Setup RBAC if enabled
	var rbacMiddleware func(http.Handler) http.Handler
	if opts.EnableRBAC {
		var err error
		rbacMiddleware, err = setupRBAC(privateRegistry, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to setup RBAC: %w", err)
		}
		logger.Info("RBAC authorization enabled")
	}

	privateRouter, err := setupRouter(privateRegistry, opts.CORSOrigins, rbacMiddleware)
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

	// Create public API if enabled
	if opts.EnablePublicAPI {
		if opts.PublicScheme == nil {
			return nil, fmt.Errorf("public scheme is required when EnablePublicAPI is true")
		}
		if len(opts.PublicResources) == 0 {
			return nil, fmt.Errorf("public resources are required when EnablePublicAPI is true")
		}

		// Public API uses separate scheme for type definitions but shares stores with private API
		// Use the same storage factory as private API
		publicRegistry := NewResourceRegistry(opts.PublicScheme, registryOpts...)
		for _, res := range opts.PublicResources {
			if err := publicRegistry.Register(res); err != nil {
				return nil, fmt.Errorf("failed to register public resource %s: %w", res.Plural, err)
			}
		}

		converter := conversion.NewConverter(opts.PublicScheme, opts.PrivateScheme)
		// Pass private registry so converting handlers can access the shared stores
		publicRouter, err := setupConvertingRouter(publicRegistry, privateRegistry, converter, opts.PrivateScheme, opts.CORSOrigins, rbacMiddleware)
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

// setupRBAC creates and returns an RBAC middleware.
// It registers RBAC resource types and creates an authorizer that uses them.
func setupRBAC(registry *ResourceRegistry, logger logr.Logger) (func(http.Handler) http.Handler, error) {
	// Register RBAC resource types
	rbacResources := []ResourceInfo{
		{
			GVK:    schema.GroupVersionKind{Group: "rbac.orlop.thetechnick.ninja", Version: "v1", Kind: "Role"},
			Plural: "roles",
		},
		{
			GVK:    schema.GroupVersionKind{Group: "rbac.orlop.thetechnick.ninja", Version: "v1", Kind: "RoleBinding"},
			Plural: "rolebindings",
		},
		{
			GVK:    schema.GroupVersionKind{Group: "rbac.orlop.thetechnick.ninja", Version: "v1", Kind: "ClusterRole"},
			Plural: "clusterroles",
		},
		{
			GVK:    schema.GroupVersionKind{Group: "rbac.orlop.thetechnick.ninja", Version: "v1", Kind: "ClusterRoleBinding"},
			Plural: "clusterrolebindings",
		},
	}

	// Register RBAC resources with the registry
	for _, res := range rbacResources {
		if err := registry.Register(res); err != nil {
			return nil, fmt.Errorf("failed to register RBAC resource %s: %w", res.Plural, err)
		}
	}

	// Add RBAC types to scheme if not already present
	if err := rbacv1.AddToScheme(registry.scheme); err != nil {
		return nil, fmt.Errorf("failed to add RBAC types to scheme: %w", err)
	}

	// Get stores for RBAC resources
	roleStore := registry.GetStore("roles")
	roleBindingStore := registry.GetStore("rolebindings")
	clusterRoleStore := registry.GetStore("clusterroles")
	clusterRoleBindingStore := registry.GetStore("clusterrolebindings")

	// Create authorizer
	authorizer := rbac.NewAuthorizer(
		roleStore,
		roleBindingStore,
		clusterRoleStore,
		clusterRoleBindingStore,
	)

	// Create and return middleware
	middleware := rbac.NewMiddleware(authorizer, logger)
	return middleware.Handler(), nil
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
