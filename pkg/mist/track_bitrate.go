package mist

// Mist reports a track's "bps" in bytes per second (DTSC::Meta::getBps) and
// the health "kbits" as getBps*8/1024, an integer Kibit/s. StreamTrack's
// bitrate_bps is bits per second and bitrate_kbps decimal kbit/s.

// TrackBitsPerSecondFromMistBps converts Mist track bps (bytes/s) to bits/s.
func TrackBitsPerSecondFromMistBps(bytesPerSecond int64) int64 {
	return bytesPerSecond * 8
}

// TrackBitsPerSecondFromMistKbits converts Mist health kbits to bits/s.
func TrackBitsPerSecondFromMistKbits(kbits float64) int64 {
	return int64(kbits * 1024)
}

// TrackKbpsFromBitsPerSecond converts bits/s to decimal kbit/s.
func TrackKbpsFromBitsPerSecond(bitsPerSecond int64) int32 {
	return int32(bitsPerSecond / 1000)
}
