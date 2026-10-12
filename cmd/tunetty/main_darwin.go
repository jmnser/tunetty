package main

import (
	"os"
	"runtime"

	"github.com/ebitengine/purego"
)

// Media key handlers are delivered on the main dispatch queue, which only the
// main thread's run loop serves. Locking here keeps main on that thread.
func init() { runtime.LockOSThread() }

// runMain runs fn on another goroutine while the main thread runs the Core
// Foundation run loop, and exits with fn's status.
func runMain(fn func() int) {
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation",
		purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		os.Exit(fn())
	}
	var runLoopRun func()
	purego.RegisterLibFunc(&runLoopRun, cf, "CFRunLoopRun")

	go func() { os.Exit(fn()) }()
	runLoopRun()
	select {} // the run loop only returns if it has nothing to serve
}
