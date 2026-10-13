//go:build darwin && cgo

package main

// The //export lives in its own file: cgo forbids definitions in a preamble next to //export.

/*
#include <stdlib.h>
*/
import "C"

//export sbAction
func sbAction(kind, id, payload *C.char) {
	k, i, p := C.GoString(kind), C.GoString(id), C.GoString(payload)
	if activeServer != nil {
		go activeServer.handleUIAction(k, i, p)
	}
}
