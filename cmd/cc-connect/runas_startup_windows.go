//go:build windows

package main

import (
	"context"

	"github.com/timmyagentic/cc-connect-next/config"
)

func runRunAsUserStartupChecks(_ context.Context, _ *config.Config) error {
	return nil
}
