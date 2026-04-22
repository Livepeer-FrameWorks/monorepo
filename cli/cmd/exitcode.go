package cmd

// ExitCodeError signals that a command wants a specific non-zero exit code
// without the default stderr error banner. main.go checks for this type via
// errors.As and exits silently with ExitCode. Commands return this from RunE
// so deferred cleanup (SSH pool, manifest temp dirs, contexts) still runs.
type ExitCodeError struct {
	Code    int
	Message string
}

func (e *ExitCodeError) Error() string { return e.Message }
func (e *ExitCodeError) ExitCode() int { return e.Code }
