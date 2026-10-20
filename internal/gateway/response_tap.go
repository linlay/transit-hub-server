package gateway

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"strings"
)

// decodingTap feeds a decoded copy of the upstream body to sink while the
// encoded bytes are forwarded to the client untouched.
type decodingTap struct {
	sink        func([]byte)
	pipe        *io.PipeWriter
	done        chan struct{}
	err         error
	unsupported bool
}

func newDecodingTap(contentEncoding string, sink func([]byte)) *decodingTap {
	tap := &decodingTap{sink: sink}
	encoding := strings.ToLower(strings.TrimSpace(contentEncoding))
	switch encoding {
	case "", "identity":
		return tap
	case "gzip", "x-gzip", "deflate":
	default:
		tap.unsupported = true
		return tap
	}
	reader, writer := io.Pipe()
	tap.pipe = writer
	tap.done = make(chan struct{})
	go func() {
		defer close(tap.done)
		err := decodeInto(encoding, reader, sink)
		if err != nil && !errors.Is(err, io.EOF) {
			tap.err = err
		}
		// Keep accepting encoded bytes so the client copy never blocks.
		_, _ = io.Copy(io.Discard, reader)
	}()
	return tap
}

func decodeInto(encoding string, encoded io.Reader, sink func([]byte)) error {
	var decoded io.Reader
	switch encoding {
	case "deflate":
		// HTTP deflate is zlib-wrapped, but some servers send raw DEFLATE.
		buffered := bufio.NewReader(encoded)
		header, err := buffered.Peek(2)
		if err != nil {
			return err
		}
		if header[0]&0x0f == 8 && (uint16(header[0])<<8|uint16(header[1]))%31 == 0 {
			zr, err := zlib.NewReader(buffered)
			if err != nil {
				return err
			}
			decoded = zr
		} else {
			decoded = flate.NewReader(buffered)
		}
	default:
		gz, err := gzip.NewReader(encoded)
		if err != nil {
			return err
		}
		decoded = gz
	}
	buffer := make([]byte, 32*1024)
	for {
		n, err := decoded.Read(buffer)
		if n > 0 {
			sink(buffer[:n])
		}
		if err != nil {
			return err
		}
	}
}

func (t *decodingTap) Write(encoded []byte) {
	if t.unsupported {
		return
	}
	if t.pipe == nil {
		t.sink(encoded)
		return
	}
	_, _ = t.pipe.Write(encoded)
}

// Close waits until every decoded byte reached sink and returns the decode
// error. A truncated body also fails decoding, so callers judge it together
// with whether the response completed.
func (t *decodingTap) Close() error {
	if t.pipe == nil {
		return nil
	}
	_ = t.pipe.Close()
	<-t.done
	return t.err
}

// decodableAcceptEncoding keeps only content codings the gateway can decode,
// so every response stays observable while remaining acceptable to the client.
func decodableAcceptEncoding(value string) string {
	kept := []string{}
	for _, part := range strings.Split(value, ",") {
		token := strings.TrimSpace(part)
		name := strings.ToLower(strings.TrimSpace(strings.SplitN(token, ";", 2)[0]))
		switch name {
		case "gzip", "x-gzip", "deflate", "identity":
			kept = append(kept, token)
		}
	}
	return strings.Join(kept, ", ")
}
