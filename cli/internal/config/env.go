package config

import "os"

// osGetenv is a package-private alias for os.Getenv so OSEnv.Get stays
// trivially swappable in tests without pulling in os.
func osGetenv(key string) string { return os.Getenv(key) }
