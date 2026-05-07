//go:build integration

package yxc

import "flag"

// yamahaHostFlag is the live receiver target. Pass via:
//
//	go test -tags=integration -yamaha-host=192.168.1.116 ./...
//
// When unset the integration tests skip rather than fail so the suite is
// safe to run unconditionally on machines without a receiver.
var yamahaHostFlag = flag.String("yamaha-host", "", "Yamaha receiver IP/host for integration tests")
