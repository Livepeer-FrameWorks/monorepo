package frameworks

import (
	"context"
	"iter"
)

// PaginateOptions bounds a paginator.
type PaginateOptions struct {
	// PageSize is the rows requested per page; 0 means 50.
	PageSize int
	// MaxItems stops after this many rows; 0 means no limit.
	MaxItems int
}

func (o PaginateOptions) pageSize() int {
	if o.PageSize > 0 {
		return o.PageSize
	}
	return 50
}

// RelayPageRequest asks for one page of a relay connection.
type RelayPageRequest struct {
	First int
	After *string
}

// RelayPage is one page of a relay connection.
type RelayPage[T any] struct {
	Nodes       []T
	HasNextPage bool
	EndCursor   *string
}

// OffsetPageRequest asks for one page of an offset-paged list.
type OffsetPageRequest struct {
	First  int
	Offset int
}

// OffsetPage is one page of an offset-paged list.
type OffsetPage[T any] struct {
	Nodes       []T
	HasNextPage bool
}

// PageTokenRequest asks for one page of a page-token list.
type PageTokenRequest struct {
	PageSize  int
	PageToken *string
}

// TokenPage is one page of a page-token list.
type TokenPage[T any] struct {
	Items         []T
	NextPageToken *string
}

// walk yields rows of successive pages until next reports no further page,
// a page is empty, or MaxItems rows were yielded. A fetch error is yielded
// once and ends the walk.
func walk[T any](opts PaginateOptions, fetch func() ([]T, bool, error), yield func(T, error) bool) {
	yielded := 0
	for {
		rows, more, err := fetch()
		if err != nil {
			var zero T
			yield(zero, err)
			return
		}
		for _, row := range rows {
			if opts.MaxItems > 0 && yielded >= opts.MaxItems {
				return
			}
			if !yield(row, nil) {
				return
			}
			yielded++
		}
		if opts.MaxItems > 0 && yielded >= opts.MaxItems {
			return
		}
		if !more || len(rows) == 0 {
			return
		}
	}
}

// PaginateRelay walks a relay connection (first/after, pageInfo.endCursor),
// e.g. streamsConnection.
func PaginateRelay[T any](ctx context.Context, fetch func(context.Context, RelayPageRequest) (RelayPage[T], error), opts PaginateOptions) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		req := RelayPageRequest{First: opts.pageSize()}
		walk(opts, func() ([]T, bool, error) {
			page, err := fetch(ctx, req)
			if err != nil {
				return nil, false, err
			}
			more := page.HasNextPage && page.EndCursor != nil && *page.EndCursor != ""
			if more {
				cursor := *page.EndCursor
				req.After = &cursor
			}
			return page.Nodes, more, nil
		}, yield)
	}
}

// PaginateOffset walks an offset-paged list (first/offset, hasNextPage),
// e.g. storageArtifactsConnection. The offset advances by the rows returned.
func PaginateOffset[T any](ctx context.Context, fetch func(context.Context, OffsetPageRequest) (OffsetPage[T], error), opts PaginateOptions) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		req := OffsetPageRequest{First: opts.pageSize()}
		walk(opts, func() ([]T, bool, error) {
			page, err := fetch(ctx, req)
			if err != nil {
				return nil, false, err
			}
			req.Offset += len(page.Nodes)
			return page.Nodes, page.HasNextPage, nil
		}, yield)
	}
}

// PaginatePageToken walks a page-token list (pageSize/pageToken,
// nextPageToken), e.g. dvrChapters.
func PaginatePageToken[T any](ctx context.Context, fetch func(context.Context, PageTokenRequest) (TokenPage[T], error), opts PaginateOptions) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		req := PageTokenRequest{PageSize: opts.pageSize()}
		walk(opts, func() ([]T, bool, error) {
			page, err := fetch(ctx, req)
			if err != nil {
				return nil, false, err
			}
			more := page.NextPageToken != nil && *page.NextPageToken != ""
			if more {
				token := *page.NextPageToken
				req.PageToken = &token
			}
			return page.Items, more, nil
		}, yield)
	}
}
