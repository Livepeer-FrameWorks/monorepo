package relay

import "errors"

// prepareMetaFixture prepares test fixtures synchronously, including retired-generation
// cleanup faults. Production uses prepareBlockStore's locked, bounded cleanup.
func (s *BlockStore) prepareMetaFixture(assetHash, ext, object string, totalSize int64) (*BlockMeta, error) {
	meta, retired, err := s.prepareMeta(assetHash, ext, object, totalSize)
	return meta, errors.Join(err, s.removeRetired(retired))
}
