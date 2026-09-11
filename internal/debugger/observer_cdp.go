package debugger

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
)

// observer_cdp.go — a tolerant reader of the server→client half of a Chrome
// DevTools Protocol WebSocket, reporting when the program pauses and resumes.
//
// It sees bytes as the socket delivers them: the HTTP 101 upgrade first, then
// frames split at arbitrary points, sometimes several to a read. It parses
// exactly enough of RFC 6455 to find text messages and read their "method",
// and nothing it sees can make it block or fail: a stream it cannot follow —
// a non-upgrade response, a compressed extension, an absurd length — is
// consumed and ignored, and the connection is proxied unchanged.

// cdpMaxMessage bounds what is buffered for one message. Debugger.paused with
// its call frames runs to a few hundred kilobytes; anything past this is not
// a pause notification and is skipped rather than held.
const cdpMaxMessage = 8 << 20

// cdpMaxHandshake bounds how much is buffered while waiting for the end of
// the HTTP response headers before giving up on the stream as a WebSocket.
const cdpMaxHandshake = 64 << 10

const (
	cdpMethodPaused  = "Debugger.paused"
	cdpMethodResumed = "Debugger.resumed"
)

// WebSocket opcodes (RFC 6455 § 5.2).
const (
	opContinuation = 0x0
	opText         = 0x1
	opClose        = 0x8
)

type cdpPhase uint8

const (
	phaseHandshake cdpPhase = iota
	phaseFrames
	phaseDead
)

type cdpObserver struct {
	phase cdpPhase
	buf   []byte // bytes not yet consumed: handshake headers, or a partial frame header

	// Current frame.
	remaining int     // payload bytes still to come
	fin       bool    // this frame ends the message
	skipping  bool    // payload is discarded (binary, control, oversized, unreadable)
	masked    bool    // server frames are never masked, but tolerate it
	mask      [4]byte // masking key
	maskPos   int     // position in the mask for the next payload byte

	// Current message.
	msg       []byte // text payload accumulated across fragments
	inMessage bool   // a text message is open, awaiting continuation frames

	// Program state as last reported, so a chunk that pauses and resumes
	// within itself reports the net change rather than both.
	paused bool
}

func newCDPObserver() *cdpObserver { return &cdpObserver{} }

// FromServer consumes b entirely and reports whether the program's state
// changed to paused or to running across it.
func (o *cdpObserver) FromServer(b []byte) (paused, resumed bool) {
	was := o.paused
	o.consume(b)
	return !was && o.paused, was && !o.paused
}

// FromServerMessage reads one whole message the bridge already decoded from
// its frame, and reports the state change the same way FromServer does.
func (o *cdpObserver) FromServerMessage(msg []byte) (paused, resumed bool) {
	was := o.paused
	o.message(msg)
	return !was && o.paused, was && !o.paused
}

func (o *cdpObserver) consume(b []byte) {
	for len(b) > 0 {
		switch o.phase {
		case phaseDead:
			return
		case phaseHandshake:
			b = o.consumeHandshake(b)
		case phaseFrames:
			b = o.consumeFrame(b)
		}
	}
}

// consumeHandshake waits for the end of the HTTP response headers. Only a
// 101 leads to frames: any other status means the connection carries plain
// HTTP — the inspector's /json/list, say — and there is nothing to watch.
func (o *cdpObserver) consumeHandshake(b []byte) []byte {
	o.buf = append(o.buf, b...)
	if len(o.buf) >= 5 && !bytes.HasPrefix(o.buf, []byte("HTTP/")) {
		o.die()
		return nil
	}
	end := bytes.Index(o.buf, []byte("\r\n\r\n"))
	if end < 0 {
		if len(o.buf) > cdpMaxHandshake {
			o.die()
		}
		return nil
	}
	status := o.buf[:end]
	if line := bytes.IndexByte(status, '\n'); line >= 0 {
		status = status[:line]
	}
	rest := append([]byte(nil), o.buf[end+4:]...)
	o.buf = nil
	if !bytes.Contains(status, []byte(" 101")) {
		o.die()
		return nil
	}
	o.phase = phaseFrames
	return rest
}

func (o *cdpObserver) die() {
	o.phase = phaseDead
	o.buf = nil
	o.msg = nil
}

// consumeFrame consumes payload for the frame in progress, or parses the next
// frame header, returning what it did not use.
func (o *cdpObserver) consumeFrame(b []byte) []byte {
	if o.remaining > 0 {
		n := min(o.remaining, len(b))
		o.payload(b[:n])
		o.remaining -= n
		if o.remaining == 0 {
			o.endFrame()
		}
		return b[n:]
	}

	o.buf = append(o.buf, b...)
	headerLen, payloadLen, ok := parseFrameHeader(o.buf)
	if !ok {
		// Not enough bytes for the header yet: keep them and wait.
		return nil
	}
	header := o.buf[:headerLen]
	rest := append([]byte(nil), o.buf[headerLen:]...)
	o.buf = nil
	o.startFrame(header, payloadLen)
	if o.remaining == 0 {
		o.endFrame()
	}
	return rest
}

// parseFrameHeader reports the header length and payload length of the
// frame at the start of b, or ok=false when b does not yet hold the whole
// header.
func parseFrameHeader(b []byte) (headerLen int, payloadLen uint64, ok bool) {
	if len(b) < 2 {
		return 0, 0, false
	}
	masked := b[1]&0x80 != 0
	headerLen = 2
	switch length := uint64(b[1] & 0x7f); {
	case length < 126:
		payloadLen = length
	case length == 126:
		headerLen += 2
		if len(b) < headerLen {
			return 0, 0, false
		}
		payloadLen = uint64(binary.BigEndian.Uint16(b[2:4]))
	default:
		headerLen += 8
		if len(b) < headerLen {
			return 0, 0, false
		}
		payloadLen = binary.BigEndian.Uint64(b[2:10])
	}
	if masked {
		headerLen += 4
		if len(b) < headerLen {
			return 0, 0, false
		}
	}
	return headerLen, payloadLen, true
}

// startFrame decides what to do with the payload that follows the header:
// collect it into the message, or skip it.
func (o *cdpObserver) startFrame(header []byte, payloadLen uint64) {
	o.fin = header[0]&0x80 != 0
	rsv := header[0] & 0x70
	opcode := header[0] & 0x0f
	o.masked = header[1]&0x80 != 0
	if o.masked {
		copy(o.mask[:], header[len(header)-4:])
	}
	o.maskPos = 0

	// A length past the cap is skipped as it streams, however large; an
	// int cannot hold the top of the uint64 range, so clamp the count and
	// keep skipping when the remainder arrives.
	if payloadLen > uint64(cdpMaxMessage) {
		o.remaining = int(min(payloadLen, uint64(math.MaxInt)))
		o.skipping = true
		o.dropMessage()
		return
	}
	o.remaining = int(payloadLen)

	switch {
	case opcode >= opClose:
		// Control frames interleave with a fragmented message and must not
		// disturb it; they carry nothing of interest.
		o.skipping = true
	case opcode == opText && rsv == 0:
		o.msg = o.msg[:0]
		o.inMessage = true
		o.skipping = false
	case opcode == opContinuation && o.inMessage:
		o.skipping = false
	default:
		// Binary, a reserved opcode, or an extension (rsv set: compressed)
		// this reader cannot decode. The message it belongs to is lost.
		o.skipping = true
		o.dropMessage()
	}
}

func (o *cdpObserver) dropMessage() {
	o.msg = o.msg[:0]
	o.inMessage = false
}

func (o *cdpObserver) payload(b []byte) {
	if o.skipping {
		return
	}
	if len(o.msg)+len(b) > cdpMaxMessage {
		o.skipping = true
		o.dropMessage()
		return
	}
	if !o.masked {
		o.msg = append(o.msg, b...)
		return
	}
	for _, c := range b {
		o.msg = append(o.msg, c^o.mask[o.maskPos])
		o.maskPos = (o.maskPos + 1) % 4
	}
}

// endFrame closes a completed frame, and with it the message if FIN was set.
func (o *cdpObserver) endFrame() {
	skipped := o.skipping
	o.skipping = false
	if skipped || !o.fin || !o.inMessage {
		return
	}
	o.inMessage = false
	o.message(o.msg)
	o.msg = o.msg[:0]
}

// message reads the method of one CDP notification. Anything that is not
// JSON with a string method — a command response, a truncated message — is
// ignored.
func (o *cdpObserver) message(msg []byte) {
	var m struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		return
	}
	switch m.Method {
	case cdpMethodPaused:
		o.paused = true
	case cdpMethodResumed:
		o.paused = false
	}
}
