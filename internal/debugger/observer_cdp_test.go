package debugger

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const handshake101 = "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n"

// frame builds one WebSocket frame by hand. RFC 6455 § 5.2: FIN and opcode
// in the first byte, the mask bit and a 7-bit length in the second, then a
// 16- or 64-bit length when the 7-bit one is 126 or 127.
func frame(fin bool, opcode byte, payload []byte, mask []byte) []byte {
	b0 := opcode
	if fin {
		b0 |= 0x80
	}
	f := []byte{b0}
	maskBit := byte(0)
	if mask != nil {
		maskBit = 0x80
	}
	switch n := len(payload); {
	case n < 126:
		f = append(f, maskBit|byte(n))
	case n < 1<<16:
		f = append(f, maskBit|126, 0, 0)
		binary.BigEndian.PutUint16(f[2:], uint16(n))
	default:
		f = append(f, maskBit|127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(f[2:], uint64(n))
	}
	if mask != nil {
		f = append(f, mask...)
		for i, c := range payload {
			f = append(f, c^mask[i%4])
		}
		return f
	}
	return append(f, payload...)
}

func textFrame(msg string) []byte { return frame(true, opText, []byte(msg), nil) }

const (
	pausedMsg  = `{"method":"Debugger.paused","params":{"callFrames":[],"reason":"other"}}`
	resumedMsg = `{"method":"Debugger.resumed"}`
	otherMsg   = `{"id":1,"result":{}}`
)

// feed delivers the stream in chunks of the given size and returns the
// sequence of verdicts that were not (false, false).
func feed(o *cdpObserver, stream []byte, chunk int) []string {
	var verdicts []string
	for len(stream) > 0 {
		n := min(chunk, len(stream))
		paused, resumed := o.FromServer(stream[:n])
		stream = stream[n:]
		switch {
		case paused:
			verdicts = append(verdicts, "paused")
		case resumed:
			verdicts = append(verdicts, "resumed")
		}
	}
	return verdicts
}

func TestCDPObserver_singleFrameAfterHandshake(t *testing.T) {
	// Given: a 101 handshake followed by one paused notification
	stream := append([]byte(handshake101), textFrame(pausedMsg)...)

	// When: it arrives in one read
	o := newCDPObserver()
	paused, resumed := o.FromServer(stream)

	// Then: the pause is reported once
	assert.True(t, paused)
	assert.False(t, resumed)
}

func TestCDPObserver_pauseAndResumeSequence(t *testing.T) {
	// Given: the handshake, then paused, an unrelated response, and resumed
	parts := [][]byte{[]byte(handshake101), textFrame(pausedMsg), textFrame(otherMsg), textFrame(resumedMsg)}

	// When: each arrives in its own read
	o := newCDPObserver()
	var verdicts []string
	for _, part := range parts {
		p, r := o.FromServer(part)
		verdicts = append(verdicts, verdict(p, r))
	}

	// Then: only the two transitions are reported, in order
	assert.Equal(t, []string{"", "paused", "", "resumed"}, verdicts)
}

func verdict(paused, resumed bool) string {
	switch {
	case paused:
		return "paused"
	case resumed:
		return "resumed"
	}
	return ""
}

func TestCDPObserver_arbitraryChunking(t *testing.T) {
	// Given: the handshake and a pause, then a resume that starts in a
	// later read — a pause that ends within the same read is no pause
	pause := append([]byte(handshake101), textFrame(pausedMsg)...)
	resume := textFrame(resumedMsg)

	for _, chunk := range []int{1, 2, 3, 7, 64, len(pause)} {
		// When: they arrive in chunks of every awkward size
		o := newCDPObserver()
		verdicts := feed(o, pause, chunk)
		verdicts = append(verdicts, feed(o, resume, chunk)...)

		// Then: the same two transitions come out
		assert.Equal(t, []string{"paused", "resumed"}, verdicts, "chunk size %d", chunk)
	}
}

func TestCDPObserver_twoMessagesInOneRead(t *testing.T) {
	// Given: a resume immediately followed by a pause in one read, after an
	// earlier pause
	o := newCDPObserver()
	o.FromServer(append([]byte(handshake101), textFrame(pausedMsg)...))

	// When: both arrive together
	paused, resumed := o.FromServer(append(textFrame(resumedMsg), textFrame(pausedMsg)...))

	// Then: the net state is unchanged, so nothing is reported
	assert.False(t, paused)
	assert.False(t, resumed)
	assert.True(t, o.paused)
}

func TestCDPObserver_fragmentedMessage(t *testing.T) {
	// Given: the paused notification split over three frames, with a ping
	// interleaved as RFC 6455 allows
	parts := []string{pausedMsg[:10], pausedMsg[10:30], pausedMsg[30:]}
	stream := []byte(handshake101)
	stream = append(stream, frame(false, opText, []byte(parts[0]), nil)...)
	stream = append(stream, frame(true, 0x9, []byte("ping"), nil)...)
	stream = append(stream, frame(false, opContinuation, []byte(parts[1]), nil)...)
	stream = append(stream, frame(true, opContinuation, []byte(parts[2]), nil)...)

	// When: it arrives byte by byte
	verdicts := feed(newCDPObserver(), stream, 1)

	// Then: the reassembled message is recognised exactly once
	assert.Equal(t, []string{"paused"}, verdicts)
}

func TestCDPObserver_16And64BitLengths(t *testing.T) {
	// Given: a paused message padded to need a 16-bit length, then a resumed
	// one padded to need a 64-bit length
	pad := func(msg string, size int) string {
		filler := strings.Repeat(" ", size-len(msg))
		return msg[:len(msg)-1] + filler + "}"
	}
	big := pad(pausedMsg, 300)
	huge := pad(resumedMsg, 70000)
	stream := []byte(handshake101)
	stream = append(stream, textFrame(big)...)
	stream = append(stream, textFrame(huge)...)
	assert.Equal(t, byte(126), stream[len(handshake101)+1], "16-bit length marker")

	// When: the stream is fed in mid-sized chunks
	verdicts := feed(newCDPObserver(), stream, 1000)

	// Then: both extended-length frames are read correctly
	assert.Equal(t, []string{"paused", "resumed"}, verdicts)
}

func TestCDPObserver_ignoresBinaryControlAndMasked(t *testing.T) {
	// Given: binary, close and pong frames carrying the paused text, and a
	// masked text frame — which servers never send, but is tolerated
	stream := []byte(handshake101)
	stream = append(stream, frame(true, 0x2, []byte(pausedMsg), nil)...)
	stream = append(stream, frame(true, 0xA, []byte(pausedMsg), nil)...)
	stream = append(stream, frame(true, opClose, []byte(pausedMsg), nil)...)
	o := newCDPObserver()
	verdicts := feed(o, stream, 5)
	assert.Empty(t, verdicts, "binary and control frames carry no state")

	// When: a masked text frame follows
	paused, _ := o.FromServer(frame(true, opText, []byte(pausedMsg), []byte{1, 2, 3, 4}))

	// Then: it is unmasked and read
	assert.True(t, paused)
}

func TestCDPObserver_nonUpgradeResponseIsIgnored(t *testing.T) {
	// Given: a plain HTTP response, as /json/list answers, whose body
	// happens to look like a frame
	stream := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n")
	stream = append(stream, textFrame(pausedMsg)...)

	// When: it is observed
	o := newCDPObserver()
	verdicts := feed(o, stream, 7)

	// Then: nothing is reported and the observer stays quiet for good
	assert.Empty(t, verdicts)
	assert.Equal(t, phaseDead, o.phase)
}

func TestCDPObserver_garbageNeverReportsOrPanics(t *testing.T) {
	// Given: streams that are not WebSockets at all
	streams := [][]byte{
		[]byte("not http at all, just bytes that go on for a while"),
		append([]byte(handshake101), 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff),
		append([]byte(handshake101), frame(true, opText, []byte("{not json"), nil)...),
		append([]byte(handshake101), frame(true, 0x1|0x40, []byte(pausedMsg), nil)...), // RSV1: compressed
		append([]byte(handshake101), frame(true, 0x3, []byte(pausedMsg), nil)...),      // reserved opcode
		append([]byte(handshake101), frame(true, opContinuation, []byte(pausedMsg), nil)...),
	}
	for i, stream := range streams {
		// When: they are observed in small chunks
		verdicts := feed(newCDPObserver(), stream, 3)

		// Then: nothing is reported
		assert.Empty(t, verdicts, "stream %d", i)
	}
}

func TestCDPObserver_oversizedMessageIsSkippedAndStreamContinues(t *testing.T) {
	// Given: a text frame declaring a payload past the cap, then a normal one
	o := newCDPObserver()
	header := []byte{0x80 | opText, 127, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint64(header[2:], uint64(cdpMaxMessage+10))
	stream := append([]byte(handshake101), header...)
	stream = append(stream, make([]byte, cdpMaxMessage+10)...)
	stream = append(stream, textFrame(pausedMsg)...)

	// When: it is streamed through
	verdicts := feed(o, stream, 1<<16)

	// Then: the oversized frame is skipped without buffering and the next
	// message is still read
	assert.Equal(t, []string{"paused"}, verdicts)
	assert.Less(t, cap(o.msg), cdpMaxMessage)
}

func TestCDPObserver_handshakeSplitFromFrames(t *testing.T) {
	// Given: the handshake ends in one read and the frame comes in the next
	o := newCDPObserver()
	_, _ = o.FromServer([]byte(handshake101[:20]))
	_, _ = o.FromServer([]byte(handshake101[20:]))

	// When: the paused frame arrives
	paused, _ := o.FromServer(textFrame(pausedMsg))

	// Then: it is read
	assert.True(t, paused)
}
