package resolvers

import (
	"strings"

	"frameworks/api_gateway/graph/model"
	commodoreclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
)

func mediaListOptionsFromInput(input *model.MediaArtifactConnectionInput) commodoreclient.MediaListOptions {
	if input == nil {
		return commodoreclient.MediaListOptions{}
	}
	opts := commodoreclient.MediaListOptions{}
	if input.Search != nil {
		opts.Search = strings.TrimSpace(*input.Search)
	}
	if input.Sort != nil {
		opts.SortField = storageArtifactSortField(*input.Sort)
	}
	if input.Direction != nil {
		opts.SortDirection = strings.ToLower(input.Direction.String())
	}
	if input.Offset != nil {
		offset := int32(*input.Offset)
		opts.Offset = &offset
	}
	return opts
}
