// The code generators of the Go SDK, kept out of the SDK module so their
// dependencies never reach SDK users. make graphql-sdk-go runs genclient,
// which drives genqlient, from here.
module github.com/Livepeer-FrameWorks/sdk-go/tools

go 1.27.0

require (
	github.com/Khan/genqlient v0.8.1
	github.com/vektah/gqlparser/v2 v2.5.19
)

require (
	github.com/agnivade/levenshtein v1.1.1 // indirect
	github.com/alexflint/go-arg v1.5.1 // indirect
	github.com/alexflint/go-scalar v1.2.0 // indirect
	github.com/bmatcuk/doublestar/v4 v4.6.1 // indirect
	golang.org/x/mod v0.35.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/tools v0.44.0 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
)
