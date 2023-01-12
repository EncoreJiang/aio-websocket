package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
)

type frameDecStatus int

const (
	frameDecHeader frameDecStatus = iota
	frameDecLength
	frameDecMaskKey
	frameDecPayload
)

// A frameHeader is a frame header as defined in hybi draft.
type frameHeader struct {
	Fin          bool
	Rsv          [3]bool
	OpCode       byte
	Length       int64
	lengthFields int
	MaskFlag     bool
	MaskingKey   []byte

	data *bytes.Buffer
}

// A frameReader is a reader for hybi frame.
type frameReader struct {
	buf    *bytes.Buffer
	status frameDecStatus

	header  frameHeader
	payload bytes.Buffer
}

func (frame *frameReader) Read(msg []byte) (n int, err error) {
	if len(frame.header.MaskingKey) == 4 {
		n = len(msg)
		for i := 0; i < n; i++ {
			msg[i] = msg[i] ^ frame.header.MaskingKey[i%4]
		}
	}
	return n, err
}

func (frame *frameReader) PayloadType() byte { return frame.header.OpCode }

func (frame *frameReader) HeaderReader() io.Reader {
	if frame.header.data == nil {
		return nil
	}
	if frame.header.data.Len() == 0 {
		return nil
	}
	return frame.header.data
}

func (frame *frameReader) TrailerReader() io.Reader { return nil }

func (frame *frameReader) Decode(msg []byte) (err error) {
	if frame.buf == nil {
		frame.buf = bytes.NewBuffer(msg)
	} else {
		frame.buf.Write(msg)
	}
	buf := frame.buf
	var header []byte
	var b byte

	log.Println("status:", frame.status)
	switch frame.status {
	case frameDecHeader:
		log.Println("frameDecHeader")
		if buf.Len() < 2 {
			return io.ErrUnexpectedEOF
		}
		// First byte. FIN/RSV1/RSV2/RSV3/OpCode(4bits)
		b, err = buf.ReadByte()
		if err != nil {
			return err
		}
		header = append(header, b)
		frame.header.Fin = ((header[0] >> 7) & 1) != 0
		for i := 0; i < 3; i++ {
			j := uint(6 - i)
			frame.header.Rsv[i] = ((header[0] >> j) & 1) != 0
		}
		frame.header.OpCode = header[0] & 0x0f

		// Second byte. Mask/Payload len(7bits)
		b, err = buf.ReadByte()
		if err != nil {
			return err
		}
		header = append(header, b)
		frame.header.MaskFlag = (b & 0x80) != 0
		frame.header.MaskingKey = frame.header.MaskingKey[:0]
		b &= 0x7f
		frame.header.lengthFields = 0
		switch {
		case b <= 125: // Payload length 7bits.
			frame.header.Length = int64(b)
		case b == 126: // Payload length 7+16bits
			frame.header.lengthFields = 2
		case b == 127: // Payload length 7+64bits
			frame.header.lengthFields = 8
		}
		frame.status = frameDecLength
		fallthrough

	case frameDecLength:
		log.Println("frameDecLength")
		if frame.header.lengthFields > 0 {
			if buf.Len() < frame.header.lengthFields {
				return io.ErrUnexpectedEOF
			}
			frame.header.Length = 0
			for i := 0; i < frame.header.lengthFields; i++ {
				b, err = buf.ReadByte()
				if err != nil {
					return err
				}
				if frame.header.lengthFields == 8 && i == 0 { // MSB must be zero when 7+64 bits
					b &= 0x7f
				}
				header = append(header, b)
				frame.header.Length = frame.header.Length*256 + int64(b)
			}
		}
		frame.status = frameDecMaskKey
		fallthrough

	case frameDecMaskKey:
		log.Println("frameDecMaskKey")
		if frame.header.MaskFlag {
			if buf.Len() < 4 {
				return io.ErrUnexpectedEOF
			}
			// Masking key. 4 bytes.
			for i := 0; i < 4; i++ {
				b, err = buf.ReadByte()
				if err != nil {
					return err
				}
				frame.header.MaskingKey = append(frame.header.MaskingKey, b)
			}
		}
		frame.status = frameDecPayload
		fallthrough

	case frameDecPayload:
		log.Println("frameDecPayload length:", frame.header.Length)
		if int64(buf.Len()) < frame.header.Length {
			return io.ErrUnexpectedEOF
		}
		frame.payload.Reset()
		frame.payload.Grow(int(frame.header.Length))
		payload := frame.payload.Bytes()[:frame.header.Length]
		n, err := buf.Read(payload)
		if err != nil {
			return err
		}
		if n != len(payload) {
			return io.ErrShortBuffer
		}
		frame.Read(payload)
		fmt.Printf("payload: %q\n", payload)
		frame.status = frameDecHeader
	}
	return nil
}
