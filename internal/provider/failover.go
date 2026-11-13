package provider

// StatusError is a provider failure with its HTTP status, so callers can tell a rate limit or an outage from a
// refused key or a request the service rejected (see Classify).
type StatusError struct {
	Status int
	Err    error
}

func (e *StatusError) Error() string { return e.Err.Error() }
func (e *StatusError) Unwrap() error { return e.Err }
