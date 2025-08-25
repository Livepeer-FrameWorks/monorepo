package graph

import "frameworks/api_gateway/graph/model"

const defaultLimit = 100
const maxLimit = 1000

func clampPagination(p *model.PaginationInput, total int) (start int, end int) {
	limit := defaultLimit
	if p != nil {
		if p.Offset != nil {
			start = *p.Offset
			if start < 0 {
				start = 0
			}
		}
		if p.Limit != nil {
			limit = *p.Limit
		}
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	end = total
	if start+limit < end {
		end = start + limit
	}
	return start, end
}
