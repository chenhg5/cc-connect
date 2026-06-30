package core

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log/slog"
)

// DefaultMaxLineSize is the default cap on a single line read by ReadLineLoop.
// Lines longer than this are logged and dropped, preventing the read loop from
// crashing on malformed or unexpectedly large input (MCP tool results, paste
// payloads, etc.).
const DefaultMaxLineSize = 10 * 1024 * 1024 // 10MB

// ReadLineLoop reads newline-delimited lines from r, invoking handle for each
// non-empty line. Lines longer than DefaultMaxLineSize are dropped with an
// error log so the loop can keep reading subsequent lines. The handle callback
// receives the line bytes with any trailing \r\n stripped.
//
// ReadLineLoop is the replacement for the bufio.Scanner pattern previously
// used by agent adapters. bufio.Scanner terminates the read loop on overflow
// (err = bufio.ErrTooLong), which kills the session; this helper logs and
// continues so a single oversized line does not abort the bridge.
//
// Returns nil on clean EOF, the underlying reader error (non-EOF), or any
// error returned by handle.
func ReadLineLoop(r io.Reader, handle func(line []byte) error) error {
	return ReadLineLoopWithLimit(r, DefaultMaxLineSize, handle)
}

// ReadLineLoopWithLimit is ReadLineLoop with a caller-specified max line size.
// maxLineSize <= 0 falls back to DefaultMaxLineSize.
func ReadLineLoopWithLimit(r io.Reader, maxLineSize int, handle func(line []byte) error) error {
	if maxLineSize <= 0 {
		maxLineSize = DefaultMaxLineSize
	}
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if len(line) > maxLineSize {
				slog.Error("ReadLineLoop: line exceeds max size, dropping",
					"size", len(line), "max", maxLineSize)
				// bufio.Reader.ReadBytes keeps reading until the next '\n'
				// or EOF, so the next iteration resumes after this oversized
				// line and processes whatever valid data follows.
			} else {
				trimmed := bytes.TrimRight(line, "\r\n")
				if len(trimmed) > 0 {
					if herr := handle(trimmed); herr != nil {
						return herr
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
