package agentproto

import (
	"bytes"
	"errors"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var b bytes.Buffer
	if err := WriteFrame(&b, FrameData, []byte("привет")); err != nil {
		t.Fatal(err)
	}
	typ, p, err := ReadFrame(&b)
	if err != nil || typ != FrameData || string(p) != "привет" {
		t.Fatal(typ, p, err)
	}
}

// Заголовок приходит из песочницы: длина не должна вызывать выделение памяти.
func TestFrameLengthIsCheckedBeforeAllocation(t *testing.T) {
	_, _, err := ReadFrame(bytes.NewReader([]byte{FrameData, 0xff, 0xff, 0xff, 0xff}))
	if !errors.Is(err, ErrFrameTooBig) {
		t.Fatal(err)
	}
	if err := WriteFrame(&bytes.Buffer{}, FrameData, make([]byte, MaxFrame+1)); !errors.Is(err, ErrFrameTooBig) {
		t.Fatal(err)
	}
}

func TestResize(t *testing.T) {
	c, r, ok := ParseResize(ResizePayload(120, 30))
	if !ok || c != 120 || r != 30 {
		t.Fatal(c, r, ok)
	}
	if _, _, ok := ParseResize([]byte{1}); ok {
		t.Fatal("короткий кадр принят")
	}
}
