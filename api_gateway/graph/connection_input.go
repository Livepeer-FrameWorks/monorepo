package graph

import "frameworks/api_gateway/graph/model"

func mergeConnectionInput(page *model.ConnectionInput, first *int, after *string, last *int, before *string) (*int, *string, *int, *string) {
	if page == nil {
		return first, after, last, before
	}
	if page.First != nil {
		first = page.First
	}
	if page.After != nil {
		after = page.After
	}
	if page.Last != nil {
		last = page.Last
	}
	if page.Before != nil {
		before = page.Before
	}
	return first, after, last, before
}

func mergeForwardConnectionInput(page *model.ConnectionInput, first *int, after *string) (*int, *string) {
	if page == nil {
		return first, after
	}
	if page.First != nil {
		first = page.First
	}
	if page.After != nil {
		after = page.After
	}
	return first, after
}
