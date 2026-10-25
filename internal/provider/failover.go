package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
)

// StatusError is a provider failure with its HTTP status, so callers can tell
// a rate limit or an outage (fall back) from a refused key (do not).
type StatusError struct {
	Status int
	Err    error
}

func (e *StatusError) Error() string { return e.Err.Error() }
func (e *StatusError) Unwrap() error { return e.Err }

// Failover tries the primary model and, when it is down rather than wrong,
// tries a secondary one. Auth and billing errors never fall back: another
// model would refuse them too.
type Failover struct {
	Primary   Provider
	Secondary Provider
	// OnFallback runs when the secondary is tried, with the primary's error.
	OnFallback func(error)
}

func (f Failover) Chat(ctx context.Context, req Request) (Message, error) {
	msg, err := f.Primary.Chat(ctx, req)
	if err == nil || ctx.Err() != nil {
		return msg, err
	}
	if !transient(err) {
		return msg, err
	}
	if f.OnFallback != nil {
		f.OnFallback(err)
	}
	msg2, err2 := f.Secondary.Chat(ctx, req)
	if err2 == nil {
		return msg2, nil
	}
	return Message{}, fmt.Errorf("%v (fallback also failed: %v)", err, err2)
}

// transient reports the failures worth retrying elsewhere: rate limits,
// overload and outages, and network errors. Anything else is permanent.
func transient(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return retryable(se.Status)
	}
	var ue *url.Error
	return errors.As(err, &ue)
}
