//go:build integration

package cli

import "flag"

// yamahaHostFlag mirrors the pkg/yxc integration flag so the CLI suite
// can be driven against the same receiver:
//
//	go test -tags=integration -yamaha-host=192.168.1.116 ./...
var yamahaHostFlag = flag.String("yamaha-host", "", "Yamaha receiver IP/host for integration tests")
