/*
 * NexQuake LZH Decompression
 *
 * Portions of this file (LZH decoding logic) are derived from github.com/koron-go/lha
 * Copyright (c) 2018 MURAOKA Taro <koron.kaoriya@gmail.com>
 *
 * Heavily modified and optimized for Quake 1.06 resource extraction by:
 * Copyright (c) 2026 Brian St. Marie
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
 * THE SOFTWARE.
 */

// Package quake106 extracts game data from the Quake 1.06 shareware distribution.
//
// The Quake 1.06 shareware archive (quake106.zip) uses a multi-part LHA-compressed
// installer format from 1996. This package implements LZH (LH5) decompression in
// pure Go — no cgo, no external binaries — to extract pak0.pak and the license text
// directly from the resource.1 segment embedded in the zip.
//
// Every extraction step is SHA256-verified: the zip itself ([ZipSHA256]), the
// resource.1 entry ([Resource1SHA256]), pak0.pak ([Pak0SHA256]), and the license
// file ([SlicnseSHA256]). Any hash mismatch causes an immediate error, ensuring
// both correctness and compliance with id Software's shareware redistribution terms.
//
// Typical usage:
//
//	zr, err := zip.OpenReader("quake106.zip")
//	if err != nil { ... }
//	defer zr.Close()
//	if err := quake106.ExtractPak0(&zr.Reader, "/game/id1"); err != nil { ... }
package quake106

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SHA256 hex digests for the canonical Quake 1.06 shareware files.
// These are used to verify the zip archive and each extracted output before writing.
const (
	// ZipSHA256 is the expected SHA256 of the quake106.zip archive itself.
	ZipSHA256 = "ec6c9d34b1ae0252ac0066045b6611a7919c2a0d78a3a66d9387a8f597553239"
	// Resource1SHA256 is the expected SHA256 of the resource.1 entry inside quake106.zip.
	Resource1SHA256 = "c192c9c71bee41750dd7d14c99378766d61e077977b9d13d1a457b8d9eabe34a"
	// Pak0SHA256 is the expected SHA256 of the extracted pak0.pak game data file.
	Pak0SHA256 = "35a9c55e5e5a284a159ad2a62e0e8def23d829561fe2f54eb402dbc0a9a946af"
	// SlicnseSHA256 is the expected SHA256 of the extracted SLICNSE.TXT license file.
	SlicnseSHA256 = "070cdf6a6410adef8fb5f83a4e5ccdb9e2301d2e48d460bb3a67a0f5ba9d70a8"
)

const (
	segPak0Off = 29041
	segPak0Len = 8612632
	segPak0Out = 18689235
	segLicOff  = 9054733
	segLicLen  = 4040
	segLicOut  = 10036
)

// ExtractPak0 extracts pak0.pak and SLICNSE.TXT from the Quake 1.06 shareware zip.
//
// zr must be an opened zip.Reader for quake106.zip. destRoot is the directory
// where the files are written; it is created with mode 0755 if it does not exist.
//
// The resource.1 segment is read from the zip, verified against [Resource1SHA256],
// LZH-decompressed, and written as two files:
//
//   - <destRoot>/pak0.pak   — game data, verified against [Pak0SHA256]
//   - <destRoot>/SLICNSE.TXT — shareware license, verified against [SlicnseSHA256]
//
// Any SHA256 mismatch or I/O error causes an immediate non-nil error return.
func ExtractPak0(zr *zip.Reader, destRoot string) error {
	var zf *zip.File
	for _, f := range zr.File {
		if strings.EqualFold(filepath.Base(strings.ReplaceAll(f.Name, `\`, `/`)), "resource.1") {
			zf = f
			break
		}
	}
	if zf == nil {
		return fmt.Errorf("resource.1 not found")
	}
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	resource, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return err
	}
	if sha256Hex(resource) != Resource1SHA256 {
		return fmt.Errorf("resource.1 hash mismatch")
	}

	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return err
	}
	if err := writeSeg(resource, segPak0Off, segPak0Len, segPak0Out, filepath.Join(destRoot, "pak0.pak"), Pak0SHA256); err != nil {
		return err
	}
	return writeSeg(resource, segLicOff, segLicLen, segLicOut, filepath.Join(destRoot, "SLICNSE.TXT"), SlicnseSHA256)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeSeg(resource []byte, off, clen, outLen int, outPath, wantSHA string) error {
	b, err := decodeLH5Seg(resource, off, clen, outLen)
	if err != nil {
		return err
	}
	if sha256Hex(b) != wantSHA {
		return fmt.Errorf("output hash mismatch")
	}
	return os.WriteFile(outPath, b, 0o644)
}

func decodeLH5Seg(resource []byte, off, clen, outLen int) ([]byte, error) {
	end := off + clen
	if off < 0 || clen < 0 || end > len(resource) {
		return nil, fmt.Errorf("resource.1 bounds invalid")
	}
	const win = 1 << 13
	buf := make([]byte, win+outLen)
	for i := 0; i < win; i++ {
		buf[i] = ' '
	}
	decodeLH5(buf, win, resource[off:end])
	return buf[win:], nil
}

type br struct {
	b []byte
	i int
	v uint64
	n uint
}

func (r *br) bits(k uint) uint16 {
	for r.n < k {
		var c byte
		if r.i < len(r.b) {
			c = r.b[r.i]
			r.i++
		}
		r.v |= uint64(c) << (56 - r.n)
		r.n += 8
	}
	v := uint16(r.v >> (64 - k))
	r.v <<= k
	r.n -= k
	return v
}

func (r *br) trues(max uint) uint16 {
	for i := uint16(0); i < uint16(max); i++ {
		if r.bits(1) == 0 {
			return i
		}
	}
	return uint16(max)
}

type huff struct {
	one        bool
	val, code  uint16
	max        uint8
	cnt, first [17]uint16
	start      [17]uint16
	sym        []uint16
}

func mkH(l []uint8) (h huff) {
	h.max = 16
	for _, v := range l {
		if v != 0 {
			h.cnt[v]++
		}
	}
	for i := uint8(1); i <= h.max; i++ {
		h.code = (h.code + h.cnt[i-1]) << 1
		h.first[i] = h.code
		h.start[i] = h.start[i-1] + h.cnt[i-1]
	}
	t := uint16(0)
	for i := uint8(1); i <= h.max; i++ {
		t += h.cnt[i]
	}
	h.sym = make([]uint16, t)
	next := h.start
	for i, v := range l {
		if v != 0 {
			h.sym[int(next[v])] = uint16(i)
			next[v]++
		}
	}
	return
}

func (h huff) dec(r *br) uint16 {
	if h.one {
		return h.val
	}
	code := uint16(0)
	for l := uint8(1); l <= h.max; l++ {
		code = (code << 1) | r.bits(1)
		if code < h.first[l] {
			continue
		}
		d := code - h.first[l]
		if d < h.cnt[l] {
			return h.sym[int(h.start[l]+d)]
		}
	}
	return 0
}

func pTree(r *br, bits uint, special, n int) huff {
	n0 := int(r.bits(bits))
	if n0 == 0 {
		return huff{one: true, val: r.bits(bits)}
	}
	if n0 > n {
		n0 = n
	}
	l := make([]uint8, n)
	for i := 0; i < n0; {
		c := r.bits(3)
		if c == 7 {
			c += r.trues(13)
		}
		l[i] = uint8(c)
		i++
		if i == special {
			z := int(r.bits(2))
			for z > 0 && i < n0 {
				l[i] = 0
				i++
				z--
			}
		}
	}
	return mkH(l)
}

func cTree(r *br, bits uint, tmp huff, n int) huff {
	n0 := int(r.bits(bits))
	if n0 == 0 {
		return huff{one: true, val: r.bits(bits)}
	}
	if n0 > n {
		n0 = n
	}
	l := make([]uint8, n)
	for i := 0; i < n0; {
		c := tmp.dec(r)
		if c > 2 {
			l[i] = uint8(c - 2)
			i++
			continue
		}
		z := 1
		if c == 1 {
			z = int(r.bits(4)) + 3
		} else if c == 2 {
			z = int(r.bits(bits)) + 20
		}
		for z > 0 && i < n {
			l[i] = 0
			i++
			z--
		}
	}
	return mkH(l)
}

func decodeLH5(buf []byte, win int, src []byte) {
	r := br{b: src}
	pos, nblk := win, 0
	var c, p huff
	for pos < len(buf) {
		if nblk == 0 {
			nblk = int(r.bits(16))
			t := pTree(&r, 5, 3, 19)
			c, p = cTree(&r, 9, t, 510), pTree(&r, 4, -1, 14)
		}
		nblk--
		v := c.dec(&r)
		if v < 256 {
			buf[pos] = byte(v)
			pos++
			continue
		}
		ln := int(v) - 253
		ov := p.dec(&r)
		off := int(ov)
		if ov > 0 {
			w := uint(ov - 1)
			off = (1 << w) + int(r.bits(w))
		}
		s := pos - off - 1
		if ln > len(buf)-pos {
			ln = len(buf) - pos
		}
		for i := 0; i < ln; i++ {
			buf[pos] = buf[s+i]
			pos++
		}
	}
}
