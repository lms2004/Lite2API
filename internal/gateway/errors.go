package gateway

import "errors"

var (
	ErrNoCapacity        = errors.New("no healthy account has available capacity")
	ErrNoEligibleAccount = errors.New("no enabled account supports the requested model and operation")
)
