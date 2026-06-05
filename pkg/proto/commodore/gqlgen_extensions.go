// GraphQL union interface markers for commodore proto types.

package commodorepb

// Stream implements union interfaces
func (*Stream) IsCreateStreamResult()      {}
func (*Stream) IsUpdateStreamResult()      {}
func (*Stream) IsSetPlaybackPolicyResult() {}

// SigningKey implements union interfaces
func (*SigningKey) IsRevokeSigningKeyResult() {}

// StreamKey implements union interfaces
func (*StreamKey) IsCreateStreamKeyResult() {}

// APITokenInfo implements union interfaces (GraphQL type: DeveloperToken)
func (*APITokenInfo) IsCreateDeveloperTokenResult() {}
