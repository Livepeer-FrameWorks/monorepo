package graph

import (
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/logging"
)

// This file will not be regenerated automatically.
//
// It serves as dependency injection for your app, add any dependencies you require here.

type Resolver struct {
	*resolvers.Resolver
}

// NewResolver creates a new GraphQL resolver using our existing resolver implementation
func NewResolver(clients *clients.ServiceClients, logger logging.Logger) *Resolver {
	return &Resolver{
		Resolver: resolvers.NewResolver(clients, logger),
	}
}
