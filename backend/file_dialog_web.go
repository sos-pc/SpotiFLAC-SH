//go:build !wails

package backend

import "context"

func SelectMultipleFiles(_ context.Context) ([]string, error) { return nil, nil }
func SelectOutputDirectory(_ context.Context) (string, error) { return "", nil }
