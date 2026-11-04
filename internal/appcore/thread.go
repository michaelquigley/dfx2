package appcore

import "runtime"

// Go runs package initialization on the startup thread. Keep main there even
// before Run; native libraries themselves must only be loaded inside Run.
func init() { runtime.LockOSThread() }
