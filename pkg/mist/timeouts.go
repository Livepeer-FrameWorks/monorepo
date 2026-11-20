package mist

import "time"

// LiveInputTimeout is the source buffer's reconnect window after its publisher ends.
const LiveInputTimeout = 12 * time.Second
