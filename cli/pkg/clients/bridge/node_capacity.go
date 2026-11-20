package bridge

import (
	"context"
	"encoding/base64"
	"fmt"
)

type NodeCapacity struct {
	EgressBudgetBytesPerSec  float64 `json:"egressBudgetBytesPerSec"`
	IngressBudgetBytesPerSec float64 `json:"ingressBudgetBytesPerSec"`
	EgressSource             string  `json:"egressSource"`
	IngressSource            string  `json:"ingressSource"`
	Revision                 uint64  `json:"revision"`
	AppliedRevision          *uint64 `json:"appliedRevision"`
}

const nodeCapacityFields = `egressBudgetBytesPerSec ingressBudgetBytesPerSec egressSource ingressSource revision appliedRevision`

func (c *Client) GetNodeCapacity(ctx context.Context, jwt, nodeID string) (*NodeCapacity, error) {
	query := `query GetNodeCapacity($id:ID!){node(id:$id){... on InfrastructureNode{capacity{` + nodeCapacityFields + `}}}}`
	var result struct {
		Node *struct {
			Capacity *NodeCapacity `json:"capacity"`
		} `json:"node"`
	}
	id := base64.StdEncoding.EncodeToString([]byte("InfrastructureNode:" + nodeID))
	if err := c.Do(ctx, query, jwt, map[string]any{"id": id}, &result); err != nil {
		return nil, err
	}
	if result.Node == nil || result.Node.Capacity == nil {
		return nil, fmt.Errorf("node capacity unavailable")
	}
	return result.Node.Capacity, nil
}

func (c *Client) SetNodeCapacity(ctx context.Context, jwt, nodeID string, egress, ingress *uint64, revision uint64) (*NodeCapacity, error) {
	query := `mutation SetNodeCapacity($input:SetNodeCapacityInput!){setNodeCapacity(input:$input){__typename ... on InfrastructureNode{capacity{` + nodeCapacityFields + `}} ... on ValidationError{message} ... on NotFoundError{message} ... on AuthError{message}}}`
	var result struct {
		SetNodeCapacity struct {
			Typename string        `json:"__typename"`
			Capacity *NodeCapacity `json:"capacity"`
			Message  string        `json:"message"`
		} `json:"setNodeCapacity"`
	}
	input := map[string]any{"nodeId": nodeID, "egressBudgetBytesPerSec": egress, "ingressBudgetBytesPerSec": ingress, "expectedRevision": revision}
	if err := c.Do(ctx, query, jwt, map[string]any{"input": input}, &result); err != nil {
		return nil, err
	}
	if result.SetNodeCapacity.Typename != "InfrastructureNode" || result.SetNodeCapacity.Capacity == nil {
		return nil, fmt.Errorf("set node capacity: %s", result.SetNodeCapacity.Message)
	}
	return result.SetNodeCapacity.Capacity, nil
}
