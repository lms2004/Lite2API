package gateway

import "errors"

var ErrNoCapacity = errors.New("no healthy account has available capacity")
