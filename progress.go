package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

func startSpinner(output io.Writer, message string) func() {
	file, ok := output.(*os.File)
	if !ok || !isTerminal(file) || os.Getenv("TERM") == "dumb" {
		return func() {}
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	fmt.Fprintf(output, "\r| %s", message)
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		frames := "|/-\\"
		frame := 0
		for {
			select {
			case <-ticker.C:
				frame = (frame + 1) % len(frames)
				fmt.Fprintf(output, "\r%c %s", frames[frame], message)
			case <-done:
				fmt.Fprintf(output, "\r%s\r", strings.Repeat(" ", len(message)+2))
				return
			}
		}
	}()
	return sync.OnceFunc(func() { close(done); <-stopped })
}
