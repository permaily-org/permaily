package mailhtml

import (
	"bytes"
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
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return pngStructure(b)
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return jpegStructure(b)
	case bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a")):
		return gifStructure(b)
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return webpStructure(b)
	}
	return imageInfo{}, false
}

// pngStructure reads the signature, IHDR first, then every chunk to IEND.
// An acTL, fcTL or fdAT chunk is APNG.
func pngStructure(b []byte) (imageInfo, bool) {
	info := imageInfo{Type: "image/png"}
	p := 8
	first := true
	for {
		if len(b)-p < 12 {
			return imageInfo{}, false
		}
		n := int(binary.BigEndian.Uint32(b[p:]))
		typ := string(b[p+4 : p+8])
		if n < 0 || n > len(b)-p-12 {
			return imageInfo{}, false
		}
		data := b[p+8 : p+8+n]
		if first {
			if typ != "IHDR" || n != 13 {
				return imageInfo{}, false
			}
			w, h := binary.BigEndian.Uint32(data), binary.BigEndian.Uint32(data[4:])
			if w == 0 || h == 0 || w > 1<<31-1 || h > 1<<31-1 {
				return imageInfo{}, false
			}
			info.Width, info.Height = int(w), int(h)
			first = false
		}
		switch typ {
		case "acTL", "fcTL", "fdAT":
			info.Animated = true
		case "IEND":
			return info, true
		}
		p += 12 + n
	}
}

// jpegStructure reads markers to the first start of frame (SOFn), which
// holds the size.
func jpegStructure(b []byte) (imageInfo, bool) {
	p := 2
	for {
		// Fill bytes before a marker.
		for p < len(b) && b[p] == 0xFF && p+1 < len(b) && b[p+1] == 0xFF {
			p++
		}
		if len(b)-p < 2 || b[p] != 0xFF {
			return imageInfo{}, false
		}
		m := b[p+1]
		p += 2
		switch {
		case m == 0xD8 || m == 0x01 || m >= 0xD0 && m <= 0xD7:
			continue // no length
		case m == 0xD9 || m == 0xDA:
			return imageInfo{}, false // end, or scan data before a frame
		}
		if len(b)-p < 2 {
			return imageInfo{}, false
		}
		n := int(binary.BigEndian.Uint16(b[p:]))
		if n < 2 || n > len(b)-p {
			return imageInfo{}, false
		}
		if m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC {
			if n < 8 {
				return imageInfo{}, false
			}
			h := int(binary.BigEndian.Uint16(b[p+3:]))
			w := int(binary.BigEndian.Uint16(b[p+5:]))
			if w == 0 || h == 0 {
				return imageInfo{}, false // a height given later (DNL) is not read
			}
			return imageInfo{Type: "image/jpeg", Width: w, Height: h}, true
		}
		p += n
	}
}

// gifStructure reads the header, the logical screen, then every block to
// the trailer. More than one image, a NETSCAPE2.0 or ANIMEXTS1.0
// application extension, is animated; an image outside the logical screen
// does not read (refused, unsupported).
func gifStructure(b []byte) (imageInfo, bool) {
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
		if p >= len(b) {
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
			if p, ok = gifSubBlocks(b, p); !ok {
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
			if p, ok = gifSubBlocks(b, p); !ok {
				return imageInfo{}, false
			}
		default:
			return imageInfo{}, false
		}
	}
}

// gifSubBlocks skips data sub-blocks to their terminator.
func gifSubBlocks(b []byte, p int) (int, bool) {
	for {
		if p >= len(b) {
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

// webpStructure reads the RIFF chunks: VP8 or VP8L alone, or VP8X with its
// animation flag clear, no ANIM or ANMF chunk, and image data.
func webpStructure(b []byte) (imageInfo, bool) {
	size := int(binary.LittleEndian.Uint32(b[4:]))
	if size < 4 || size > len(b)-8 {
		return imageInfo{}, false
	}
	b = b[:8+size]
	info := imageInfo{Type: "image/webp"}
	p := 12
	first := true
	extended, haveData := false, false
	for p < len(b) {
		if len(b)-p < 8 {
			return imageInfo{}, false
		}
		fourcc := string(b[p : p+4])
		n := int(binary.LittleEndian.Uint32(b[p+4:]))
		if n < 0 || n > len(b)-p-8 {
			return imageInfo{}, false
		}
		data := b[p+8 : p+8+n]
		switch fourcc {
		case "VP8X":
			if !first || n < 10 {
				return imageInfo{}, false
			}
			extended = true
			if data[0]&0x02 != 0 {
				info.Animated = true
			}
			info.Width = 1 + int(uint32(data[4])|uint32(data[5])<<8|uint32(data[6])<<16)
			info.Height = 1 + int(uint32(data[7])|uint32(data[8])<<8|uint32(data[9])<<16)
		case "VP8 ":
			if n < 10 || data[3] != 0x9D || data[4] != 0x01 || data[5] != 0x2A {
				return imageInfo{}, false
			}
			w := int(binary.LittleEndian.Uint16(data[6:]) & 0x3FFF)
			h := int(binary.LittleEndian.Uint16(data[8:]) & 0x3FFF)
			if !extended {
				info.Width, info.Height = w, h
			}
			haveData = true
		case "VP8L":
			if n < 5 || data[0] != 0x2F {
				return imageInfo{}, false
			}
			bits := binary.LittleEndian.Uint32(data[1:])
			w := int(bits&0x3FFF) + 1
			h := int(bits>>14&0x3FFF) + 1
			if !extended {
				info.Width, info.Height = w, h
			}
			haveData = true
		case "ANIM", "ANMF":
			info.Animated = true
		}
		if first && !extended && fourcc != "VP8 " && fourcc != "VP8L" {
			return imageInfo{}, false
		}
		first = false
		p += 8 + n + n&1
	}
	if p != len(b) && p != len(b)+1 {
		return imageInfo{}, false
	}
	if !haveData && !info.Animated {
		return imageInfo{}, false
	}
	if info.Width == 0 || info.Height == 0 {
		return imageInfo{}, false
	}
	return info, true
}
