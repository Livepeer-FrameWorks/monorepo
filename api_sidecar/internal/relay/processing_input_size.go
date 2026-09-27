package relay

import "sync"

// processingInputSizes holds the byte size of each processing input the relay
// admitted into its disk block cache, keyed by asset hash. A URL import has no
// other point where its source is sized on the node: Mist reads it through
// the relay, which learns the size from the tenant's server.
var processingInputSizes sync.Map

func recordProcessingInputSize(assetHash string, sizeBytes int64) {
	if assetHash == "" || sizeBytes <= 0 {
		return
	}
	processingInputSizes.Store(assetHash, sizeBytes)
}

// TakeProcessingInputSize returns and forgets the staged size of assetHash's
// processing input.
func TakeProcessingInputSize(assetHash string) (int64, bool) {
	v, ok := processingInputSizes.LoadAndDelete(assetHash)
	if !ok {
		return 0, false
	}
	size, ok := v.(int64)
	return size, ok
}
