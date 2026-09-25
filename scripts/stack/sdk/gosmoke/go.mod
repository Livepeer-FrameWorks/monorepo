module stacksmoke

go 1.27.0

require github.com/Livepeer-FrameWorks/sdk-go v0.0.0

require (
	github.com/Khan/genqlient v0.8.1 // indirect
	github.com/Livepeer-FrameWorks/monorepo/pkg v0.0.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/standard-webhooks/standard-webhooks/libraries v0.0.1 // indirect
	github.com/vektah/gqlparser/v2 v2.5.19 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace (
	github.com/Livepeer-FrameWorks/monorepo/pkg => ../../../../pkg
	github.com/Livepeer-FrameWorks/sdk-go => ../../../../sdk_go
)
