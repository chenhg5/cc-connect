//go:build windows

package main

import (
	"context"

	"github.com/chenhg5/cc-connect/config"
)

func runDoctor(_ []string) {}

func runRunAsUserStartupChecks(_ context.Context, _ *config.Config) error { return nil }
