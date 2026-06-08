package graph

import (
	"context"
	"errors"

	"github.com/99designs/gqlgen/complexity"
	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/errcode"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

const complexityExtensionName = "ScalarFreeComplexityLimit"

// errComplexityLimit matches gqlgen's stock code so clients see the same error code.
const errComplexityLimit = "COMPLEXITY_LIMIT_EXCEEDED"

// ScalarFreeComplexityLimit is a query-complexity guard equivalent to gqlgen's
// extension.ComplexityLimit, but it computes cost with scalar and enum leaves valued at
// zero (complexity.WithFixedScalarValue(0)). That makes complexity reflect object
// structure and per-row fetch work (see PerItemFetchCost) rather than how many cheap
// scalars a row projects — the Shopify/GitHub model. The stock extension hard-codes
// scalars at 1 and stores its options unexported, so it cannot express this.
type ScalarFreeComplexityLimit struct {
	Func func(ctx context.Context, opCtx *graphql.OperationContext) int

	es graphql.ExecutableSchema
}

var _ interface {
	graphql.OperationContextMutator
	graphql.HandlerExtension
} = &ScalarFreeComplexityLimit{}

func (c ScalarFreeComplexityLimit) ExtensionName() string {
	return complexityExtensionName
}

func (c *ScalarFreeComplexityLimit) Validate(schema graphql.ExecutableSchema) error {
	if c.Func == nil {
		return errors.New("ScalarFreeComplexityLimit func can not be nil")
	}
	c.es = schema
	return nil
}

func (c ScalarFreeComplexityLimit) MutateOperationContext(
	ctx context.Context,
	opCtx *graphql.OperationContext,
) *gqlerror.Error {
	op := opCtx.Doc.Operations.ForName(opCtx.OperationName)
	cost := complexity.Calculate(ctx, c.es, op, opCtx.Variables, complexity.WithFixedScalarValue(0))

	limit := c.Func(ctx, opCtx)
	if cost > limit {
		err := gqlerror.Errorf(
			"operation has complexity %d, which exceeds the limit of %d",
			cost,
			limit,
		)
		errcode.Set(err, errComplexityLimit)
		return err
	}

	return nil
}
