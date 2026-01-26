// Package graphql provides shared GraphQL schema and operations.
// The operations are embedded at compile time for use by both the
// SvelteKit frontend (via Houdini) and the Go API gateway (via go:embed).
package graphql

import "embed"

// OperationsFS provides access to the embedded .gql operation files.
// These files are the single source of truth for GraphQL operations
// used by both the frontend and the MCP API integration tools.
//
// Structure:
//
//	operations/
//	  queries/*.gql       - Query operations
//	  mutations/*.gql     - Mutation operations
//	  subscriptions/*.gql - Subscription operations
//	  fragments/*.gql     - Reusable fragments
//
//go:embed operations/queries/*.gql operations/mutations/*.gql operations/subscriptions/*.gql operations/fragments/*.gql
var OperationsFS embed.FS
