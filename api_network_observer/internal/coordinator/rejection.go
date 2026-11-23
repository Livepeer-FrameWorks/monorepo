package coordinator

import "errors"

type observationRejection struct{ code, message string }

func (e *observationRejection) Error() string { return e.message }
func reject(code, message string) error       { return &observationRejection{code: code, message: message} }
func rejectionCode(err error) string {
	var rejected *observationRejection
	if errors.As(err, &rejected) {
		return rejected.code
	}
	return "OBSERVATION_INVALID"
}
