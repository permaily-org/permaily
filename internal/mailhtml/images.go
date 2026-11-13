package mailhtml

import (
	"bytes"
	"context"
	"encoding/binary"
)

// Reading an image's structure without decoding a pixel (ADR 0026,
// "Images"): headers, chunk and block lengths, never pixel data. What the
// browser would have to decode is known from here: its size and whether it
// is animated. Nothing is decoded or re-encoded in the enclave.

// imageInfo is what an image's structure says.
type imageInfo struct {
	Type     string // "image/png", "image/jpeg", "image/gif", "image/webp"
	Width    int
	Height   int
	Animated bool
}

// Why an image is not shown, as contracts.MessageImagesOmitted counts it.
const (
	omitTooLarge    = "too_large"
	omitAnimated    = "animated"
	omitUnsupported = "unsupported"
)

// imageStructure reads b's structure. ok is false when b is not PNG, JPEG,
// GIF or WebP by its bytes, or its structure does not read to its end.
func imageStructure(b []byte) (imageInfo, bool) {
	info, ok, _ := readImage(context.Background(), b)
	return info, ok
}

// readImage is imageStructure under ctx: every loop of the walkers checks
// it, and err is its error once it has ended.
func readImage(ctx context.Context, b []byte) (info imageInfo, ok bool, err error) {
	s := &imageSteps{ctx: ctx}
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		info, ok = pngStructure(s, b)
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		info, ok = jpegStructure(s, b)
	case bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a")):
		info, ok = gifStructure(s, b)
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		info, ok = webpStructure(s, b)
	}
	if s.err != nil {
		return imageInfo{}, false, s.err
	}
	return info, ok, nil
}

// imageSteps counts the steps of a walker and checks its context every
// ctxEvery of them; stop says the walk must end (and err why).
type imageSteps struct {
	ctx context.Context
	n   int
	err error
}

func (s *imageSteps) stop() bool {
	if s.n++; s.n%ctxEvery == 0 && s.err == nil {
		s.err = s.ctx.Err()
	}
	return s.err != nil
}

// The walkers are structural checks, not validators: they accept a file
// only when its structure says one thing, so the size they measure is the
// size a decoder decodes (one header, one image, nothing after its end that
// a decoder could read as another). An accepted file can still fail to
// decode in a browser, which then shows nothing.

// pngStructure reads the signature, IHDR first and once, then every chunk
// to IEND: PLTE once and before the image data, the IDAT chunks one after
// another, at least one, and nothing after IEND. An acTL, fcTL or fdAT
// chunk is APNG.
func pngStructure(s *imageSteps, b []byte) (imageInfo, bool) {
	info := imageInfo{Type: "image/png"}
	p := 8
	idat, idatEnded, plte := false, false, false
	for first := true; ; first = false {
		if s.stop() || len(b)-p < 12 {
			return imageInfo{}, false
		}
		n := int(binary.BigEndian.Uint32(b[p:]))
		typ := string(b[p+4 : p+8])
		if n < 0 || n > len(b)-p-12 {
			return imageInfo{}, false
		}
		data := b[p+8 : p+8+n]
		if first != (typ == "IHDR") {
			return imageInfo{}, false // IHDR first, and only first
		}
		if idat && typ != "IDAT" {
			idatEnded = true
		}
		switch typ {
		case "IHDR":
			if n != 13 {
				return imageInfo{}, false
			}
			w, h := binary.BigEndian.Uint32(data), binary.BigEndian.Uint32(data[4:])
			if w == 0 || h == 0 || w > 1<<31-1 || h > 1<<31-1 {
				return imageInfo{}, false
			}
			info.Width, info.Height = int(w), int(h)
		case "IDAT":
			if idatEnded {
				return imageInfo{}, false
			}
			idat = true
		case "PLTE":
			if plte || idat {
				return imageInfo{}, false
			}
			plte = true
		case "acTL", "fcTL", "fdAT":
			info.Animated = true
		case "IEND":
			if !idat || p+12+n != len(b) {
				return imageInfo{}, false
			}
			return info, true
		}
		p += 12 + n
	}
}

// jpegStructure reads every marker from the start of the image to its end
// (EOI), the entropy-coded data of each scan skipped to the next marker:
// exactly one frame header (SOFn), before the first scan, and no second
// start of image. What follows EOI is not read; a decoder stops there.
func jpegStructure(s *imageSteps, b []byte) (imageInfo, bool) {
	var info imageInfo
	p := 2
	frames, scans := 0, 0
	for {
		// Fill bytes before a marker.
		for p+1 < len(b) && b[p] == 0xFF && b[p+1] == 0xFF {
			p++
		}
		if s.stop() || len(b)-p < 2 || b[p] != 0xFF {
			return imageInfo{}, false
		}
		m := b[p+1]
		p += 2
		switch {
		case m == 0xD9:
			if frames != 1 || scans == 0 {
				return imageInfo{}, false
			}
			return info, true
		case m == 0xD8 || m == 0xDE || m == 0xDF:
			return imageInfo{}, false // another image, or a hierarchical one
		case m == 0x01 || m >= 0xD0 && m <= 0xD7:
			continue // no length
		}
		if len(b)-p < 2 {
			return imageInfo{}, false
		}
		n := int(binary.BigEndian.Uint16(b[p:]))
		if n < 2 || n > len(b)-p {
			return imageInfo{}, false
		}
		if m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC {
			frames++
			if frames > 1 || scans > 0 || n < 8 {
				return imageInfo{}, false
			}
			h := int(binary.BigEndian.Uint16(b[p+3:]))
			w := int(binary.BigEndian.Uint16(b[p+5:]))
			if w == 0 || h == 0 {
				return imageInfo{}, false // a height given later (DNL) is not read
			}
			info = imageInfo{Type: "image/jpeg", Width: w, Height: h}
		}
		p += n
		if m != 0xDA {
			continue
		}
		if frames == 0 {
			return imageInfo{}, false // scan data before a frame
		}
		scans++
		// The entropy-coded data, to the next marker: 0xFF followed by
		// 0x00 (a stuffed byte), a restart marker or more fill is data.
		for {
			i := bytes.IndexByte(b[p:], 0xFF)
			if s.stop() || i < 0 || p+i+1 >= len(b) {
				return imageInfo{}, false
			}
			p += i
			if next := b[p+1]; next == 0x00 || next >= 0xD0 && next <= 0xD7 {
				p += 2
				continue
			}
			break
		}
	}
}

// gifStructure reads the header, the logical screen, then every block to
// the trailer. More than one image, a NETSCAPE2.0 or ANIMEXTS1.0
// application extension, is animated; an image outside the logical screen
// does not read (refused, unsupported).
func gifStructure(s *imageSteps, b []byte) (imageInfo, bool) {
	if len(b) < 13 {
		return imageInfo{}, false
	}
	info := imageInfo{Type: "image/gif"}
	info.Width = int(binary.LittleEndian.Uint16(b[6:]))
	info.Height = int(binary.LittleEndian.Uint16(b[8:]))
	if info.Width == 0 || info.Height == 0 {
		return imageInfo{}, false
	}
	p := 13
	if b[10]&0x80 != 0 {
		p += 3 << (int(b[10]&7) + 1)
	}
	images := 0
	for {
		if s.stop() || p >= len(b) {
			return imageInfo{}, false
		}
		switch b[p] {
		case 0x3B:
			if images == 0 {
				return imageInfo{}, false
			}
			return info, true
		case 0x2C:
			if len(b)-p < 10 {
				return imageInfo{}, false
			}
			left := int(binary.LittleEndian.Uint16(b[p+1:]))
			top := int(binary.LittleEndian.Uint16(b[p+3:]))
			w := int(binary.LittleEndian.Uint16(b[p+5:]))
			h := int(binary.LittleEndian.Uint16(b[p+7:]))
			packed := b[p+9]
			p += 10
			if packed&0x80 != 0 {
				p += 3 << (int(packed&7) + 1)
			}
			if left+w > info.Width || top+h > info.Height {
				return imageInfo{}, false // a frame outside the screen: refused
			}
			images++
			if images > 1 {
				info.Animated = true
			}
			p++ // LZW minimum code size
			var ok bool
			if p, ok = gifSubBlocks(s, b, p); !ok {
				return imageInfo{}, false
			}
		case 0x21:
			if len(b)-p < 2 {
				return imageInfo{}, false
			}
			label := b[p+1]
			p += 2
			if label == 0xFF && p < len(b) && int(b[p]) == 11 && len(b)-p >= 12 {
				app := string(b[p+1 : p+12])
				if app == "NETSCAPE2.0" || app == "ANIMEXTS1.0" {
					info.Animated = true
				}
			}
			var ok bool
			if p, ok = gifSubBlocks(s, b, p); !ok {
				return imageInfo{}, false
			}
		default:
			return imageInfo{}, false
		}
	}
}

// gifSubBlocks skips data sub-blocks to their terminator.
func gifSubBlocks(s *imageSteps, b []byte, p int) (int, bool) {
	for {
		if s.stop() || p >= len(b) {
			return 0, false
		}
		n := int(b[p])
		p++
		if n == 0 {
			return p, true
		}
		if n > len(b)-p {
			return 0, false
		}
		p += n
	}
}

// webpStructure reads the RIFF chunks. The simple form is one chunk, VP8
// or VP8L, and nothing else. The extended form is VP8X first and once,
// then, in this order and each at most once: ICCP, ALPH (only before a
// lossy VP8), the image data (exactly one VP8 or VP8L, whose size is
// VP8X's canvas), EXIF, XMP. Any other chunk, or one repeated or out of
// that order, refuses the file. The animation flag, or an ANIM or ANMF
// chunk, is animation.
func webpStructure(s *imageSteps, b []byte) (imageInfo, bool) {
	size := int(binary.LittleEndian.Uint32(b[4:]))
	if size < 4 || size > len(b)-8 {
		return imageInfo{}, false
	}
	b = b[:8+size]
	type chunk struct {
		fourcc string
		data   []byte
	}
	var chunks []chunk
	p := 12
	for p < len(b) {
		if s.stop() || len(b)-p < 8 {
			return imageInfo{}, false
		}
		n := int(binary.LittleEndian.Uint32(b[p+4:]))
		if n < 0 || n > len(b)-p-8 {
			return imageInfo{}, false
		}
		chunks = append(chunks, chunk{string(b[p : p+4]), b[p+8 : p+8+n]})
		p += 8 + n + n&1
	}
	if len(chunks) == 0 || p != len(b) && p != len(b)+1 {
		return imageInfo{}, false
	}
	info := imageInfo{Type: "image/webp"}
	if chunks[0].fourcc != "VP8X" {
		if len(chunks) != 1 {
			return imageInfo{}, false
		}
		w, h, ok := webpPayload(chunks[0].fourcc, chunks[0].data)
		if !ok {
			return imageInfo{}, false
		}
		info.Width, info.Height = w, h
		return info, true
	}
	x := chunks[0].data
	if len(x) < 10 {
		return imageInfo{}, false
	}
	info.Width = 1 + int(uint32(x[4])|uint32(x[5])<<8|uint32(x[6])<<16)
	info.Height = 1 + int(uint32(x[7])|uint32(x[8])<<8|uint32(x[9])<<16)
	for _, c := range chunks[1:] {
		if c.fourcc == "ANIM" || c.fourcc == "ANMF" {
			info.Animated = true
		}
	}
	if x[0]&0x02 != 0 || info.Animated {
		info.Animated = true
		return info, true // never shown: its frames are not read
	}
	// The order of the extended form, as a position each chunk must pass.
	order := map[string]int{"ICCP": 1, "ALPH": 2, "VP8 ": 3, "VP8L": 3, "EXIF": 4, "XMP ": 5}
	at, payloads, alpha := 0, 0, false
	for _, c := range chunks[1:] {
		pos, ok := order[c.fourcc]
		if !ok || pos <= at && pos != 3 || pos == 3 && payloads > 0 {
			return imageInfo{}, false // unknown, repeated or out of order
		}
		at = pos
		switch c.fourcc {
		case "ALPH":
			alpha = true
		case "VP8 ", "VP8L":
			if c.fourcc == "VP8L" && alpha {
				return imageInfo{}, false // an alpha chunk a lossless image ignores
			}
			w, h, ok := webpPayload(c.fourcc, c.data)
			if !ok || w != info.Width || h != info.Height {
				return imageInfo{}, false
			}
			payloads++
		}
	}
	if payloads != 1 {
		return imageInfo{}, false
	}
	return info, true
}

// webpPayload is the size a VP8 (lossy key frame) or VP8L (lossless) chunk
// declares.
func webpPayload(fourcc string, data []byte) (int, int, bool) {
	switch fourcc {
	case "VP8 ":
		if len(data) < 10 || data[3] != 0x9D || data[4] != 0x01 || data[5] != 0x2A {
			return 0, 0, false
		}
		w := int(binary.LittleEndian.Uint16(data[6:]) & 0x3FFF)
		h := int(binary.LittleEndian.Uint16(data[8:]) & 0x3FFF)
		return w, h, w > 0 && h > 0
	case "VP8L":
		if len(data) < 5 || data[0] != 0x2F {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(data[1:])
		return int(bits&0x3FFF) + 1, int(bits>>14&0x3FFF) + 1, true
	}
	return 0, 0, false
}
