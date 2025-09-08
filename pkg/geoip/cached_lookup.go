package geoip

import (
	"context"

	"frameworks/pkg/cache"
)

// LookupCached performs a GeoIP lookup using an external cache when provided.
// Falls back to direct lookup if cache is nil or reader not initialized.
func LookupCached(ctx context.Context, reader *Reader, c *cache.Cache, ip string) *GeoData {
	if reader == nil {
		return nil
	}
	if c == nil {
		return reader.Lookup(ip)
	}
	val, ok, _ := c.Get(ctx, ip, func(ctx context.Context, key string) (interface{}, bool, error) {
		gd := reader.Lookup(key)
		if gd == nil {
			return nil, false, nil
		}
		return gd, true, nil
	})
	if !ok {
		return nil
	}
	if gd, _ := val.(*GeoData); gd != nil {
		return gd
	}
	return nil
}
