// GraphQL union interface markers for shared proto types.

package sharedpb

// ClipInfo implements union interfaces (GraphQL type: Clip)
func (*ClipInfo) IsCreateClipResult()        {}
func (*ClipInfo) IsSetPlaybackPolicyResult() {}

// DVRInfo implements union interfaces (GraphQL type: DVRRequest)
func (*DVRInfo) IsStartDVRResult() {}
