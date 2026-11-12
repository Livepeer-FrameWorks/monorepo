package bridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	graphqlops "github.com/Livepeer-FrameWorks/monorepo/pkg/graphql"
)

type AuthSession struct {
	ID            string    `json:"id"`
	ClientKind    string    `json:"clientKind"`
	ClientID      string    `json:"clientId"`
	AuthMethod    string    `json:"authMethod"`
	Label         string    `json:"label"`
	ClientVersion *string   `json:"clientVersion"`
	IPAddress     *string   `json:"ipAddress"`
	Location      *string   `json:"location"`
	CreatedAt     time.Time `json:"createdAt"`
	LastActiveAt  time.Time `json:"lastActiveAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
	Current       bool      `json:"current"`
}

type AuthSessionsPage struct {
	Nodes      []AuthSession `json:"nodes"`
	TotalCount int           `json:"totalCount"`
	PageInfo   struct {
		EndCursor   *string `json:"endCursor"`
		HasNextPage bool    `json:"hasNextPage"`
	} `json:"pageInfo"`
}

type SessionRevoked struct {
	Success bool `json:"success"`
	Current bool `json:"current"`
}

type SessionActionError struct{ Code, Message string }

func (e *SessionActionError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "account security action was refused"
}

func sessionOperation(file string, fragments ...string) (string, error) {
	data, err := graphqlops.OperationsFS.ReadFile("operations/" + file + ".gql")
	if err != nil {
		return "", fmt.Errorf("read shared security operation: %w", err)
	}
	var query strings.Builder
	query.Write(data)
	for _, fragment := range fragments {
		data, err = graphqlops.OperationsFS.ReadFile("operations/fragments/" + fragment + ".gql")
		if err != nil {
			return "", fmt.Errorf("read shared security fragment: %w", err)
		}
		query.WriteByte('\n')
		query.Write(data)
	}
	return query.String(), nil
}

func (c *Client) AuthSessions(ctx context.Context, token string, first int, after *string) (*AuthSessionsPage, error) {
	query, err := sessionOperation("queries/GetAuthSessions", "AuthSessionFields")
	if err != nil {
		return nil, err
	}
	var response struct {
		Page *AuthSessionsPage `json:"authSessionsConnection"`
	}
	if err = c.Do(ctx, query, token, map[string]any{"first": first, "after": after}, &response); err != nil {
		return nil, err
	}
	if response.Page == nil {
		return nil, fmt.Errorf("bridge returned no session data; session management requires platform v0.3.15 or newer")
	}
	return response.Page, nil
}

func (c *Client) RevokeAuthSession(ctx context.Context, token, id string) (*SessionRevoked, error) {
	query, err := sessionOperation("mutations/RevokeAuthSession", "AuthErrorFields")
	if err != nil {
		return nil, err
	}
	var response struct {
		Result struct {
			SessionRevoked
			Type    string `json:"__typename"`
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"revokeAuthSession"`
	}
	if err = c.Do(ctx, query, token, map[string]any{"id": id}, &response); err != nil {
		return nil, err
	}
	if response.Result.Type != "RevokeAuthSessionPayload" {
		return nil, &SessionActionError{Code: response.Result.Code, Message: response.Result.Message}
	}
	if !response.Result.Success {
		return nil, fmt.Errorf("bridge did not revoke the session")
	}
	return &response.Result.SessionRevoked, nil
}

func (c *Client) RevokeOtherAuthSessions(ctx context.Context, token string) (int, error) {
	query, err := sessionOperation("mutations/RevokeOtherAuthSessions", "AuthErrorFields")
	if err != nil {
		return 0, err
	}
	var response struct {
		Result struct {
			Type         string `json:"__typename"`
			Message      string `json:"message"`
			Code         string `json:"code"`
			RevokedCount int    `json:"revokedCount"`
		} `json:"revokeOtherAuthSessions"`
	}
	if err = c.Do(ctx, query, token, nil, &response); err != nil {
		return 0, err
	}
	if response.Result.Type != "RevokeOtherAuthSessionsPayload" {
		return 0, &SessionActionError{Code: response.Result.Code, Message: response.Result.Message}
	}
	return response.Result.RevokedCount, nil
}
