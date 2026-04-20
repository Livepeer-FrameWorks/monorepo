package bridge

import (
	"context"
	"fmt"
)

// EdgeBootstrapInput mirrors the GraphQL BootstrapEdgeInput.
type EdgeBootstrapInput struct {
	Token           string `json:"token"`
	ExternalIP      string `json:"externalIp,omitempty"`
	PreferredNodeID string `json:"preferredNodeId,omitempty"`
}

// EdgeTelemetrySetup mirrors the GraphQL EdgeTelemetrySetup.
type EdgeTelemetrySetup struct {
	Enabled     bool   `json:"enabled"`
	WriteURL    string `json:"writeUrl,omitempty"`
	BearerToken string `json:"bearerToken,omitempty"`
}

// EdgeBootstrapResult mirrors BootstrapEdgeResponse — the resolved
// payload an edge needs to come online.
type EdgeBootstrapResult struct {
	NodeID           string              `json:"nodeId"`
	EdgeDomain       string              `json:"edgeDomain"`
	PoolDomain       string              `json:"poolDomain,omitempty"`
	ClusterSlug      string              `json:"clusterSlug"`
	ClusterID        string              `json:"clusterId"`
	FoghornGRPCAddr  string              `json:"foghornGrpcAddr"`
	CertPEM          string              `json:"certPem,omitempty"`
	KeyPEM           string              `json:"keyPem,omitempty"`
	InternalCABundle string              `json:"internalCaBundle,omitempty"`
	Telemetry        *EdgeTelemetrySetup `json:"telemetry,omitempty"`
}

// BootstrapEdge calls the public bootstrapEdge mutation. No JWT — the
// bootstrap token in the input is the credential.
func (c *Client) BootstrapEdge(ctx context.Context, in EdgeBootstrapInput) (*EdgeBootstrapResult, error) {
	const query = `
mutation BootstrapEdge($input: BootstrapEdgeInput!) {
  bootstrapEdge(input: $input) {
    __typename
    ... on BootstrapEdgeResponse {
      nodeId
      edgeDomain
      poolDomain
      clusterSlug
      clusterId
      foghornGrpcAddr
      certPem
      keyPem
      internalCaBundle
      telemetry { enabled writeUrl bearerToken }
    }
    ... on ValidationError { message field }
    ... on AuthError { message }
  }
}`
	var resp struct {
		BootstrapEdge struct {
			Typename string `json:"__typename"`
			EdgeBootstrapResult
			Message string `json:"message,omitempty"`
		} `json:"bootstrapEdge"`
	}
	if err := c.Do(ctx, query, "", map[string]any{"input": in}, &resp); err != nil {
		return nil, err
	}
	switch resp.BootstrapEdge.Typename {
	case "BootstrapEdgeResponse":
		out := resp.BootstrapEdge.EdgeBootstrapResult
		return &out, nil
	case "ValidationError", "AuthError":
		return nil, fmt.Errorf("bootstrapEdge: %s", resp.BootstrapEdge.Message)
	default:
		return nil, fmt.Errorf("bootstrapEdge: unexpected response type %q", resp.BootstrapEdge.Typename)
	}
}

// CreateEdgeClusterInput mirrors the GraphQL CreateEdgeClusterInput.
type CreateEdgeClusterInput struct {
	ClusterName      string  `json:"clusterName"`
	ShortDescription *string `json:"shortDescription,omitempty"`
}

// CreatedEdgeCluster is the subset of CreateEdgeClusterResponse the CLI
// consumes — cluster identity, the bootstrap token to hand to an edge,
// and the cluster's assigned Foghorn (returned for parity with the
// underlying QM response, though edges resolve it via bootstrapEdge).
type CreatedEdgeCluster struct {
	ClusterID        string `json:"clusterId"`
	ClusterName      string `json:"clusterName"`
	BootstrapToken   string `json:"bootstrapToken"`
	BootstrapTokenID string `json:"bootstrapTokenId"`
	FoghornAddr      string `json:"foghornAddr"`
}

// CreateEdgeCluster calls the createEdgeCluster mutation. JWT required.
func (c *Client) CreateEdgeCluster(ctx context.Context, jwt string, in CreateEdgeClusterInput) (*CreatedEdgeCluster, error) {
	const query = `
mutation CreateEdgeCluster($input: CreateEdgeClusterInput!) {
  createEdgeCluster(input: $input) {
    __typename
    ... on CreateEdgeClusterResponse {
      cluster { id clusterId clusterName }
      bootstrapToken { id token }
      foghornAddr
    }
    ... on ValidationError { message field }
    ... on AuthError { message }
  }
}`
	var resp struct {
		CreateEdgeCluster struct {
			Typename string `json:"__typename"`
			Cluster  struct {
				ID          string `json:"id"`
				ClusterID   string `json:"clusterId"`
				ClusterName string `json:"clusterName"`
			} `json:"cluster"`
			BootstrapToken struct {
				ID    string `json:"id"`
				Token string `json:"token"`
			} `json:"bootstrapToken"`
			FoghornAddr string `json:"foghornAddr"`
			Message     string `json:"message,omitempty"`
		} `json:"createEdgeCluster"`
	}
	if err := c.Do(ctx, query, jwt, map[string]any{"input": in}, &resp); err != nil {
		return nil, err
	}
	switch resp.CreateEdgeCluster.Typename {
	case "CreateEdgeClusterResponse":
		return &CreatedEdgeCluster{
			ClusterID:        resp.CreateEdgeCluster.Cluster.ClusterID,
			ClusterName:      resp.CreateEdgeCluster.Cluster.ClusterName,
			BootstrapToken:   resp.CreateEdgeCluster.BootstrapToken.Token,
			BootstrapTokenID: resp.CreateEdgeCluster.BootstrapToken.ID,
			FoghornAddr:      resp.CreateEdgeCluster.FoghornAddr,
		}, nil
	case "ValidationError", "AuthError":
		return nil, fmt.Errorf("createEdgeCluster: %s", resp.CreateEdgeCluster.Message)
	default:
		return nil, fmt.Errorf("createEdgeCluster: unexpected response type %q", resp.CreateEdgeCluster.Typename)
	}
}

// CreatedEnrollmentToken carries just the token string; the rich
// metadata (kind, expiry, etc.) is on the server side.
type CreatedEnrollmentToken struct {
	Token   string
	TokenID string
}

// CreateEnrollmentToken calls the createEnrollmentToken mutation. JWT required.
func (c *Client) CreateEnrollmentToken(ctx context.Context, jwt, clusterID string, name, ttl *string) (*CreatedEnrollmentToken, error) {
	const query = `
mutation CreateEnrollmentToken($clusterId: ID!, $name: String, $ttl: String) {
  createEnrollmentToken(clusterId: $clusterId, name: $name, ttl: $ttl) {
    __typename
    ... on CreateEnrollmentTokenResponse { bootstrapToken { id token } }
    ... on ValidationError { message field }
    ... on AuthError { message }
  }
}`
	vars := map[string]any{"clusterId": clusterID}
	if name != nil {
		vars["name"] = *name
	}
	if ttl != nil {
		vars["ttl"] = *ttl
	}
	var resp struct {
		CreateEnrollmentToken struct {
			Typename       string `json:"__typename"`
			BootstrapToken struct {
				ID    string `json:"id"`
				Token string `json:"token"`
			} `json:"bootstrapToken"`
			Message string `json:"message,omitempty"`
		} `json:"createEnrollmentToken"`
	}
	if err := c.Do(ctx, query, jwt, vars, &resp); err != nil {
		return nil, err
	}
	switch resp.CreateEnrollmentToken.Typename {
	case "CreateEnrollmentTokenResponse":
		return &CreatedEnrollmentToken{
			Token:   resp.CreateEnrollmentToken.BootstrapToken.Token,
			TokenID: resp.CreateEnrollmentToken.BootstrapToken.ID,
		}, nil
	case "ValidationError", "AuthError":
		return nil, fmt.Errorf("createEnrollmentToken: %s", resp.CreateEnrollmentToken.Message)
	default:
		return nil, fmt.Errorf("createEnrollmentToken: unexpected response type %q", resp.CreateEnrollmentToken.Typename)
	}
}
