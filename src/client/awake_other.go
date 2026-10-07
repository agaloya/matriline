//go:build !linux && !darwin && !windows

package main

import "errors"

func preventSleep() (func(), error) { return nil, errors.New("not supported on this system") }
