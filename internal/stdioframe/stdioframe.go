// Package stdioframe reads and writes JSON-RPC messages on a byte stream in
// either of the two framings an MCP stdio peer may use.
//
// MCP's stdio transport is newline-delimited: one JSON message per line, no
// embedded newlines (spec, Transports: stdio). mcpkit originally shipped
// LSP-style Content-Length framing instead, so the server keeps accepting it
// and detects which one the client speaks from the first byte (#1517).
package stdioframe

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync/atomic"

	gohttp "github.com/panyam/servicekit/http"
)

// Framing selects how messages are delimited on the stream.
type Framing int32

const (
	// Auto detects the framing from the first message: a leading '{' or '['
	// means NewlineDelimited, anything else ContentLength. Writers treat Auto
	// as NewlineDelimited until detection has happened.
	Auto Framing = iota
	// NewlineDelimited is the MCP spec framing: one JSON message per line.
	NewlineDelimited
	// ContentLength is LSP-style framing: "Content-Length: N\r\n\r\n<body>".
	ContentLength
)

// String returns the framing's name.
func (f Framing) String() string {
	switch f {
	case Auto:
		return "auto"
	case NewlineDelimited:
		return "newline-delimited"
	case ContentLength:
		return "content-length"
	}
	return fmt.Sprintf("Framing(%d)", int32(f))
}

// Reader reads messages in a fixed or detected framing. Read is not safe for
// concurrent use; Framing is.
type Reader struct {
	r    *bufio.Reader
	mode atomic.Int32
}

// NewReader returns a Reader over r. With Auto, the framing is fixed by the
// first message read and never changes afterwards.
func NewReader(r io.Reader, mode Framing) *Reader {
	fr := &Reader{r: bufio.NewReader(r)}
	fr.mode.Store(int32(mode))
	return fr
}

// Framing reports the framing in use: the configured one, or for Auto the
// detected one, or Auto if nothing has been read yet.
func (fr *Reader) Framing() Framing { return Framing(fr.mode.Load()) }

// Read returns the next message body. It returns io.EOF on a clean end of
// stream. In NewlineDelimited mode blank lines are skipped, a trailing "\r" is
// dropped, and a final line with no terminating newline is still returned.
func (fr *Reader) Read() ([]byte, error) {
	if fr.Framing() == Auto {
		mode, err := fr.detect()
		if err != nil {
			return nil, err
		}
		fr.mode.Store(int32(mode))
	}
	if fr.Framing() == ContentLength {
		return gohttp.ReadFrame(fr.r)
	}
	for {
		line, err := fr.r.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			return line, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// detect skips leading whitespace and peeks at the first significant byte.
func (fr *Reader) detect() (Framing, error) {
	for {
		b, err := fr.r.ReadByte()
		if err != nil {
			return Auto, err
		}
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		}
		if err := fr.r.UnreadByte(); err != nil {
			return Auto, err
		}
		if b == '{' || b == '[' {
			return NewlineDelimited, nil
		}
		return ContentLength, nil
	}
}

// Write writes one message to w in the given framing, Auto meaning
// NewlineDelimited. A newline-delimited message containing a line break is
// compacted first, since a raw message passed through unmarshalled may carry
// one and would otherwise split into two lines on the wire. The message goes
// out in a single Write call; callers sharing w still need their own mutex.
func Write(w io.Writer, mode Framing, data []byte) error {
	if mode == ContentLength {
		return gohttp.WriteFrame(w, data)
	}
	var buf bytes.Buffer
	if bytes.ContainsAny(data, "\r\n") {
		if err := json.Compact(&buf, data); err != nil {
			return fmt.Errorf("compact message for newline-delimited framing: %w", err)
		}
	} else {
		buf.Grow(len(data) + 1)
		buf.Write(data)
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}
