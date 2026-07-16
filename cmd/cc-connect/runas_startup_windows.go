//go:build windows

package main

import (
	"context"

	"github.com/JayGarland/cc-connect/config"
)

func runRunAsUserStartupChecks(_ context.Context, _ *config.Config) error {
	return nil
}
