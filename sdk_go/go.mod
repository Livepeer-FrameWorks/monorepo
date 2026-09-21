module github.com/Livepeer-FrameWorks/sdk-go

go 1.27.0

require (
	github.com/Khan/genqlient v0.8.1
	github.com/Livepeer-FrameWorks/monorepo/pkg v0.0.0
	github.com/gorilla/websocket v1.5.3
	github.com/standard-webhooks/standard-webhooks/libraries v0.0.1
	github.com/vektah/gqlparser/v2 v2.5.19
	google.golang.org/protobuf v1.36.12
)

require github.com/google/uuid v1.6.0 // indirect

// Development inside the monorepo resolves pkg from the working tree.
// scripts/publish-packages.sh replaces this with the pseudo-version of the
// released monorepo commit before it pushes the sdk-go mirror.
replace github.com/Livepeer-FrameWorks/monorepo/pkg => ../pkg
