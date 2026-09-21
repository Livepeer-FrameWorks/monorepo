package frameworks

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type paginationFixture struct {
	Cases []struct {
		Name           string           `json:"name"`
		Strategy       string           `json:"strategy"`
		PageSize       int              `json:"pageSize"`
		MaxItems       int              `json:"maxItems"`
		ItemsField     string           `json:"itemsField"`
		Pages          []map[string]any `json:"pages"`
		ExpectRequests []map[string]any `json:"expectRequests"`
		ExpectItems    []any            `json:"expectItems"`
	} `json:"cases"`
}

func strPtrOrNil(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

func derefOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func TestPaginationStrategies(t *testing.T) {
	var fx paginationFixture
	loadFixture(t, "pagination.json", &fx)
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			pages := tc.Pages
			var requests []map[string]any
			nextPage := func(req map[string]any) (map[string]any, error) {
				requests = append(requests, req)
				if len(pages) == 0 {
					return nil, errors.New("paginator requested a page past the last one")
				}
				p := pages[0]
				pages = pages[1:]
				return p, nil
			}
			opts := PaginateOptions{PageSize: tc.PageSize, MaxItems: tc.MaxItems}
			ctx := context.Background()
			var seq func(func(any, error) bool)
			switch tc.Strategy {
			case "relay":
				seq = PaginateRelay(ctx, func(_ context.Context, r RelayPageRequest) (RelayPage[any], error) {
					p, err := nextPage(map[string]any{"first": r.First, "after": derefOrNil(r.After)})
					if err != nil {
						return RelayPage[any]{}, err
					}
					info := p["pageInfo"].(map[string]any)
					return RelayPage[any]{Nodes: p["nodes"].([]any), HasNextPage: info["hasNextPage"].(bool), EndCursor: strPtrOrNil(info["endCursor"])}, nil
				}, opts)
			case "offset":
				seq = PaginateOffset(ctx, func(_ context.Context, r OffsetPageRequest) (OffsetPage[any], error) {
					p, err := nextPage(map[string]any{"first": r.First, "offset": r.Offset})
					if err != nil {
						return OffsetPage[any]{}, err
					}
					return OffsetPage[any]{Nodes: p["nodes"].([]any), HasNextPage: p["hasNextPage"].(bool)}, nil
				}, opts)
			case "pageToken":
				seq = PaginatePageToken(ctx, func(_ context.Context, r PageTokenRequest) (TokenPage[any], error) {
					p, err := nextPage(map[string]any{"pageSize": r.PageSize, "pageToken": derefOrNil(r.PageToken)})
					if err != nil {
						return TokenPage[any]{}, err
					}
					return TokenPage[any]{Items: p[tc.ItemsField].([]any), NextPageToken: strPtrOrNil(p["nextPageToken"])}, nil
				}, opts)
			default:
				t.Fatalf("unknown strategy %s", tc.Strategy)
			}
			items := []any{}
			for item, err := range seq {
				if err != nil {
					t.Fatal(err)
				}
				items = append(items, item)
			}
			if !jsonEqual(items, tc.ExpectItems) {
				t.Errorf("items = %v, want %v", items, tc.ExpectItems)
			}
			if !jsonEqual(requests, tc.ExpectRequests) {
				t.Errorf("requests = %v, want %v", requests, tc.ExpectRequests)
			}
		})
	}
}

// jsonEqual compares two values by their JSON form.
func jsonEqual(a, b any) bool {
	var x, y any
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	_ = json.Unmarshal(ab, &x)
	_ = json.Unmarshal(bb, &y)
	return reflect.DeepEqual(x, y)
}
