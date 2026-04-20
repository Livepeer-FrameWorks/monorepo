package credentials

import "os"

func osGetenv(key string) string { return os.Getenv(key) }
