package main

import (
	"errors"
	"runtime"
	"syscall"
)

// preventSleep calls SetThreadExecutionState(ES_CONTINUOUS | ES_SYSTEM_REQUIRED): the
// system does not sleep while the calling thread keeps that state; the display may turn
// off. The state belongs to the thread, so one locked goroutine holds it.
func preventSleep() (func(), error) {
	const esContinuous, esSystemRequired = 0x80000000, 0x00000001
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("SetThreadExecutionState")
	if err := proc.Find(); err != nil {
		return nil, err
	}
	release, ok := make(chan struct{}), make(chan bool)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		r, _, _ := proc.Call(esContinuous | esSystemRequired)
		ok <- r != 0
		<-release
		proc.Call(esContinuous)
	}()
	if !<-ok {
		close(release)
		return nil, errors.New("SetThreadExecutionState failed")
	}
	return func() { close(release) }, nil
}
