package control

import (
	"context"
	"errors"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
)

type dvrWriterBindingKey struct{}

var errStaleDVRWriter = errors.New("recording report belongs to a previous source writer")

func dvrWriterReportContext(ctx context.Context, runtime string) context.Context {
	return context.WithValue(ctx, dvrWriterBindingKey{}, runtime)
}

func dvrWriterReportMatches(ctx context.Context, expected string) bool {
	if _, isolated := streamident.ParseRuntimeBinding(expected); !isolated {
		return true
	}
	reported, nodeReport := ctx.Value(dvrWriterBindingKey{}).(string)
	return !nodeReport || reported == expected
}
