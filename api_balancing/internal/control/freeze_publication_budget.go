package control

import "time"

// freezePublicationTimeout budgets a provider-side copy from its verified size.
// The upper bound stays below the stale-freeze recovery window.
func freezePublicationTimeout(size int64) time.Duration {
	const floor = time.Minute
	const ceiling = 15 * time.Minute
	const bytesPerSecond = int64(8 << 20)
	if size <= 0 {
		return floor
	}
	seconds := size / bytesPerSecond
	if size%bytesPerSecond != 0 {
		seconds++
	}
	if seconds >= int64((ceiling-floor)/time.Second) {
		return ceiling
	}
	return floor + time.Duration(seconds)*time.Second
}
