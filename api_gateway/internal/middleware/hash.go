package middleware

import "hash/fnv"

func hashIdentifier(value string) uint64 {
	if value == "" {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	return h.Sum64()
}

// HashIdentifier exposes the internal hash for other middleware consumers.
func HashIdentifier(value string) uint64 {
	return hashIdentifier(value)
}
