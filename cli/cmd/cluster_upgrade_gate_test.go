package cmd

// testDiscard is an io.Writer sink for cobra command output in gate/classification tests.
type testDiscard struct{}

func (testDiscard) Write(p []byte) (int, error) { return len(p), nil }
