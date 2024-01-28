package main

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
)

func smallStacktrace() string {
	const unicodeEllipsis = "\u2026"

	var buf bytes.Buffer
	pc := make([]uintptr, 100)
	pc = pc[:runtime.Callers(2, pc)]
	frames := runtime.CallersFrames(pc)
	for {
		f, more := frames.Next()

		if idx := strings.LastIndexByte(f.Function, '/'); idx >= 0 {
			f.Function = f.Function[idx+1:]
		}

		// trim full path to at most 30 characters
		fullPath := fmt.Sprintf("%s:%-4d", f.File, f.Line)
		if len(fullPath) > 30 {
			fullPath = unicodeEllipsis + fullPath[len(fullPath)-29:]
		}

		fmt.Fprintf(&buf, "%30s %s\n", fullPath, f.Function)

		if !more {
			return buf.String()
		}
	}
	return buf.String()
}
