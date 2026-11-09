package ntgpuimage

// Fuzz targets for the code that runs without a GPU: CPU mip generation, the
// sRGB encoder, readback row copying, and size and mip-chain validation.
// `task fuzz-smoke` runs each briefly; the NimbleTerminal fuzzing fleet
// (fuzz.nimbleterminal.com) runs them continuously with ntfuzz, one process on
// one CPU and no GPU. Keep every target headless, deterministic, and cheap per
// execution, and list new targets in FUZZ_TARGETS in Taskfile.yml.

import (
	"bytes"
	"errors"
	"image"
	"math"
	"math/big"
	"testing"
)

// fuzzMaxSide bounds fuzzed image sides so one execution stays fast while
// still covering odd sizes, 1×N tails, and chains several levels deep.
const fuzzMaxSide = 64

// fuzzFill repeats data across dst; empty data leaves dst unchanged.
func fuzzFill(dst, data []byte) {
	if len(data) == 0 {
		return
	}
	for i := range dst {
		dst[i] = data[i%len(data)]
	}
}

// fuzzSource builds a w×h image of the given kind from data and returns it
// with its backing pixels, so a target can prove the source was not modified.
func fuzzSource(data []byte, w, h int, kind uint8) (image.Image, []byte) {
	switch kind % 4 {
	case 1:
		// A sub-image: nonzero origin, padded stride, and pixels outside the bounds.
		parent := image.NewNRGBA(image.Rect(-3, -5, w+2, h+1))
		fuzzFill(parent.Pix, data)
		return parent.SubImage(image.Rect(-1, -2, w-1, h-2)), parent.Pix
	case 2:
		// Premultiplied, including out-of-gamut values with color above alpha.
		img := image.NewRGBA(image.Rect(7, 9, 7+w, 9+h))
		fuzzFill(img.Pix, data)
		return img, img.Pix
	case 3:
		img := image.NewGray(image.Rect(0, 0, w, h))
		fuzzFill(img.Pix, data)
		return img, img.Pix
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	fuzzFill(img.Pix, data)
	return img, img.Pix
}

// expectWithinLevel checks that a level is an average of the level above it:
// every channel lies within the range of the texels that can contribute.
// Alpha-weighted color ignores fully transparent texels, and a texel that
// only they cover is transparent black.
func expectWithinLevel(t *testing.T, level int, prev, cur *image.NRGBA, opts MipOptions) {
	t.Helper()
	lo, hi := [4]byte{255, 255, 255, 255}, [4]byte{}
	colored := false
	for i := 0; i < len(prev.Pix); i += 4 {
		p := prev.Pix[i : i+4]
		lo[3], hi[3] = min(lo[3], p[3]), max(hi[3], p[3])
		if opts.AlphaWeighted && p[3] == 0 {
			continue
		}
		colored = true
		for c := range 3 {
			lo[c], hi[c] = min(lo[c], p[c]), max(hi[c], p[c])
		}
	}
	for i := 0; i < len(cur.Pix); i += 4 {
		p := cur.Pix[i : i+4]
		if p[3] < lo[3] || p[3] > hi[3] {
			t.Fatalf("level %d pixel %d: alpha %d outside source range [%d, %d]", level, i/4, p[3], lo[3], hi[3])
		}
		if opts.AlphaWeighted && p[0] == 0 && p[1] == 0 && p[2] == 0 && p[3] == 0 {
			continue
		}
		for c := range 3 {
			if !colored || p[c] < lo[c] || p[c] > hi[c] {
				t.Fatalf("level %d pixel %d channel %d: %d outside source range [%d, %d] (colored source: %v)", level, i/4, c, p[c], lo[c], hi[c], colored)
			}
		}
	}
}

// FuzzMipChain checks MipChain against the general area filter byte for byte,
// and checks the chain's shape, its averaging bounds, and that the source
// survives untouched, across source image types, sizes, and options.
func FuzzMipChain(f *testing.F) {
	f.Add([]byte{255, 0, 0, 255, 0, 255, 0, 0}, uint8(3), uint8(3), uint8(0))
	f.Add([]byte{1, 2, 3, 0, 250, 251, 252, 1, 9, 9, 9, 127}, uint8(4), uint8(2), uint8(3))
	f.Add([]byte{}, uint8(0), uint8(0), uint8(2))
	f.Add([]byte{200, 100, 50, 128, 7}, uint8(6), uint8(0), uint8(1|1<<2))
	f.Add([]byte{255, 255, 255, 3, 0, 0, 0, 255, 90}, uint8(0), uint8(12), uint8(3|2<<2))
	f.Add([]byte{0, 17, 255, 128}, uint8(63), uint8(62), uint8(1|3<<2))
	f.Fuzz(func(t *testing.T, data []byte, w8, h8, flags uint8) {
		w, h := int(w8)%fuzzMaxSide+1, int(h8)%fuzzMaxSide+1
		opts := MipOptions{SRGB: flags&1 != 0, AlphaWeighted: flags&2 != 0}
		src, backing := fuzzSource(data, w, h, flags>>2)
		before := bytes.Clone(backing)

		got, err := MipChain(src, opts)
		if err != nil {
			t.Fatalf("MipChain %dx%d: %v", w, h, err)
		}
		if !bytes.Equal(backing, before) {
			t.Fatal("MipChain modified its source")
		}
		if len(got) != mipLevelCount(w, h) {
			t.Fatalf("%dx%d: %d levels, mipLevelCount says %d", w, h, len(got), mipLevelCount(w, h))
		}
		if lw, lh := levelSize(w, h, len(got)-1); lw != 1 || lh != 1 {
			t.Fatalf("%dx%d: last level %d is %dx%d by levelSize", w, h, len(got)-1, lw, lh)
		}
		expectSameChain(t, "area reference", got, referenceMipChain(src, opts))

		levels := make([]*image.NRGBA, len(got))
		for i := range got {
			levels[i] = got[i].(*image.NRGBA) // expectSameChain verified the type.
			lw, lh := levelSize(w, h, i)
			if levels[i].Rect != image.Rect(0, 0, lw, lh) || levels[i].Stride != lw*4 || len(levels[i].Pix) != lw*lh*4 {
				t.Fatalf("level %d: rect %v stride %d len %d, want tight %dx%d", i, levels[i].Rect, levels[i].Stride, len(levels[i].Pix), lw, lh)
			}
			if i > 0 {
				expectWithinLevel(t, i, levels[i-1], levels[i], opts)
			}
		}
		if !bytes.Equal(levels[0].Pix, packPixels(src)) {
			t.Fatal("level 0 is not the packed source")
		}
		// The chain must be accepted as a complete set of levels for upload.
		if lw, lh, err := validateLevels("fuzz", got); err != nil || lw != w || lh != h {
			t.Fatalf("validateLevels on MipChain output: %dx%d, %v", lw, lh, err)
		}
		pixels, err := uploadPixels(src, true, opts)
		if err != nil || len(pixels) != len(levels) {
			t.Fatalf("uploadPixels: %d levels, %v", len(pixels), err)
		}
		for i := range pixels {
			if !bytes.Equal(pixels[i], levels[i].Pix) {
				t.Fatalf("uploadPixels level %d differs from MipChain", i)
			}
		}
	})
}

// FuzzEncodeSRGB checks the table-driven encoder against the exact transfer
// function everywhere, concentrating on the guard bands around each rounding
// threshold, and checks that it is monotonic and inverts the decode table.
func FuzzEncodeSRGB(f *testing.F) {
	f.Add(0.0, 1.0)
	f.Add(0.0031308, 0.0031308+srgbGuard)
	f.Add(0.5, 0.5)
	f.Add(0.25, 0.75)
	f.Add(math.NaN(), math.Inf(1))
	f.Add(-1.0, 2.0)
	f.Add(math.SmallestNonzeroFloat64, math.Nextafter(1, 0))
	f.Add(srgbThreshold[10], srgbThreshold[11])
	f.Add(srgbThreshold[254], math.Nextafter(srgbThreshold[0], 0))
	f.Fuzz(func(t *testing.T, v, w float64) {
		check := func(x float64) {
			if got, want := encodeSRGB(x), encodeSRGBExact(x); got != want {
				t.Fatalf("encodeSRGB(%v [%#x]) = %d, exact encoder says %d", x, math.Float64bits(x), got, want)
			}
		}
		check(v)
		check(w)
		// Steer one input into the neighborhood of a threshold, inside and just
		// outside the guard band, where the fast path hands over to the exact one.
		if v >= 0 && v <= 1 {
			n := math.Float64bits(w) % uint64(len(srgbThreshold))
			check(srgbThreshold[n] + (v-0.5)*4*srgbGuard)
			check(math.Nextafter(srgbThreshold[n], v))
		}
		if v >= 0 && v <= w && w <= 1 && encodeSRGB(v) > encodeSRGB(w) {
			t.Fatalf("not monotonic: encodeSRGB(%v) = %d > encodeSRGB(%v) = %d", v, encodeSRGB(v), w, encodeSRGB(w))
		}
		if b := byte(math.Float64bits(v)); encodeSRGB(srgbLinear[b]) != b {
			t.Fatalf("encodeSRGB(srgbLinear[%d]) = %d", b, encodeSRGB(srgbLinear[b]))
		}
	})
}

// FuzzCopyRows checks the readback copies. validateDestination, which guards
// the caller-supplied image of ReadPixelsInto, must accept exactly the images
// whose rows fit their pixel buffer; copyImageRows and depad must then write
// exactly the rows, or fail without writing.
func FuzzCopyRows(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4}, uint8(2), uint8(2), int8(0), int8(0), 8, uint16(16), int16(0), int16(0), uint8(0))
	f.Add([]byte{9}, uint8(3), uint8(4), int8(0), int8(0), 20, uint16(72), int16(-5), int16(11), uint8(0))
	f.Add([]byte{9}, uint8(3), uint8(4), int8(0), int8(0), 20, uint16(71), int16(0), int16(0), uint8(0))
	f.Add([]byte{}, uint8(5), uint8(1), int8(0), int8(0), math.MaxInt, uint16(20), int16(0), int16(0), uint8(0))
	f.Add([]byte{7, 8}, uint8(5), uint8(2), int8(0), int8(0), math.MaxInt, uint16(65535), int16(0), int16(0), uint8(0))
	f.Add([]byte{7, 8}, uint8(4), uint8(4), int8(1), int8(0), 16, uint16(64), int16(0), int16(0), uint8(0))
	f.Add([]byte{7, 8}, uint8(4), uint8(4), int8(0), int8(-1), 16, uint16(64), int16(0), int16(0), uint8(0))
	f.Add([]byte{7, 8}, uint8(4), uint8(4), int8(0), int8(0), -16, uint16(64), int16(0), int16(0), uint8(0))
	f.Add([]byte{7, 8}, uint8(4), uint8(4), int8(0), int8(0), 16, uint16(64), int16(0), int16(0), uint8(1))
	f.Add([]byte{7, 8}, uint8(0), uint8(3), int8(0), int8(0), 0, uint16(0), int16(0), int16(0), uint8(0))
	f.Add([]byte{7, 8}, uint8(3), uint8(0), int8(0), int8(0), 12, uint16(12), int16(0), int16(0), uint8(0))
	f.Fuzz(func(t *testing.T, data []byte, w8, h8 uint8, dw, dh int8, stride int, pixLen uint16, ox, oy int16, short uint8) {
		const sentinel = 0xA5
		w, h := int(w8)%(fuzzMaxSide+1), int(h8)%(fuzzMaxSide+1)
		row := w * 4

		srcStride := alignedBytesPerRow(w)
		if srcStride%rowAlign != 0 || srcStride < row || srcStride >= row+rowAlign {
			t.Fatalf("alignedBytesPerRow(%d) = %d", w, srcStride)
		}
		// The smallest source that holds h padded rows, so an overread panics.
		var src []byte
		if h > 0 {
			src = make([]byte, srcStride*(h-1)+row)
		}
		for i := range src {
			src[i] = byte(i*31 + 1)
		}
		fuzzFill(src, data)
		cut := min(int(short), len(src))
		src = src[:len(src)-cut]

		dst := &image.NRGBA{
			Pix:    bytes.Repeat([]byte{sentinel}, int(pixLen)),
			Stride: stride,
			Rect:   image.Rect(int(ox), int(oy), int(ox)+w+int(dw), int(oy)+h+int(dh)),
		}
		// Independent of the implementation's overflow-avoiding division.
		need := new(big.Int).Mul(big.NewInt(int64(stride)), big.NewInt(int64(h-1)))
		need.Add(need, big.NewInt(int64(row)))
		valid := w > 0 && h > 0 && dst.Rect.Dx() == w && dst.Rect.Dy() == h && stride >= row && need.Cmp(big.NewInt(int64(pixLen))) <= 0

		err := validateDestination(dst, w, h)
		if (err == nil) != valid {
			t.Fatalf("validateDestination(rect %v stride %d len %d, %dx%d) = %v, want valid=%v", dst.Rect, stride, pixLen, w, h, err, valid)
		}
		if validateDestination(nil, w, h) == nil {
			t.Fatal("validateDestination accepted a nil image")
		}

		want := bytes.Clone(dst.Pix)
		err = copyImageRows(dst, src, w, h, srcStride)
		if ok := valid && cut == 0; (err == nil) != ok {
			t.Fatalf("copyImageRows(valid=%v, src short by %d) = %v", valid, cut, err)
		} else if ok {
			for y := range h {
				copy(want[y*stride:y*stride+row], src[y*srcStride:y*srcStride+row])
			}
		}
		if !bytes.Equal(dst.Pix, want) {
			t.Fatalf("copyImageRows (err %v) wrote outside the %dx%d rows at stride %d, or wrote on failure", err, w, h, stride)
		}

		tight := bytes.Repeat([]byte{sentinel}, row*h+3)
		want = bytes.Clone(tight)
		err = depad(tight[:row*h], src, w, h, srcStride)
		if (err == nil) != (cut == 0) {
			t.Fatalf("depad %dx%d (src short by %d) = %v", w, h, cut, err)
		} else if err == nil {
			for y := range h {
				copy(want[y*row:(y+1)*row], src[y*srcStride:y*srcStride+row])
			}
		}
		if !bytes.Equal(tight, want) {
			t.Fatalf("depad %dx%d (err %v) wrote the wrong bytes", w, h, err)
		}
		if row*h > 0 {
			if err := depad(tight[:row*h-1], src, w, h, srcStride); err == nil {
				t.Fatalf("depad %dx%d accepted a destination one byte short", w, h)
			}
		}
	})
}

// FuzzValidateLevels checks the validation in front of UploadTextureLevels and
// UpdateLevels: a chain is accepted exactly when its base has a positive size
// whose packed byte count fits an int and every level is the next halving,
// for any bounds origin, without reading a pixel.
func FuzzValidateLevels(f *testing.F) {
	f.Add(0, 0, 4, 4, uint8(3), uint8(0), int8(0), int8(0))
	f.Add(-7, 3, 5, 3, uint8(3), uint8(1), int8(0), int8(1))
	f.Add(0, 0, 1, 1, uint8(1), uint8(0), int8(0), int8(0))
	f.Add(0, 0, 1, 1, uint8(2), uint8(0), int8(0), int8(0))
	f.Add(0, 0, 8, 1, uint8(0), uint8(0), int8(0), int8(0))
	f.Add(0, 0, 0, 4, uint8(1), uint8(0), int8(0), int8(0))
	f.Add(0, 0, -4, -4, uint8(1), uint8(0), int8(0), int8(0))
	f.Add(1, 1, math.MaxInt, 1, uint8(1), uint8(0), int8(0), int8(0))
	f.Add(0, 0, math.MaxInt/4, 1, uint8(40), uint8(0), int8(0), int8(0))
	f.Add(0, 0, math.MaxInt/4+1, 1, uint8(1), uint8(0), int8(0), int8(0))
	f.Add(math.MaxInt, math.MinInt, 1<<16, 1<<14, uint8(17), uint8(9), int8(-1), int8(0))
	f.Add(0, 0, 6, 6, uint8(3), uint8(1), int8(-3), int8(0))
	f.Fuzz(func(t *testing.T, x0, y0, w, h int, n8, bad uint8, dw, dh int8) {
		// Bounds are built directly so the origin can sit anywhere, including
		// where Max wraps; Dx and Dy are still exactly the level size.
		level := func(x, y, lw, lh int) image.Image {
			return &boundsOnlyImage{rect: image.Rectangle{Min: image.Pt(x, y), Max: image.Pt(x+lw, y+lh)}}
		}
		fits := func(lw, lh int) bool {
			if lw <= 0 || lh <= 0 {
				return false
			}
			size := new(big.Int).Mul(big.NewInt(int64(lw)), big.NewInt(int64(lh)))
			return size.Lsh(size, 2).Cmp(big.NewInt(math.MaxInt)) <= 0
		}

		gw, gh, err := imageSize("fuzz", level(x0, y0, w, h))
		if (err == nil) != fits(w, h) || (err == nil && (gw != w || gh != h)) {
			t.Fatalf("imageSize(%dx%d) = %dx%d, %v; fits=%v", w, h, gw, gh, err, fits(w, h))
		}

		count := 0 // Levels in a full chain, by repeated halving.
		if fits(w, h) {
			for lw, lh := w, h; ; lw, lh = max(1, lw/2), max(1, lh/2) {
				count++
				if lw == 1 && lh == 1 {
					break
				}
			}
			if got := mipLevelCount(w, h); got != count {
				t.Fatalf("mipLevelCount(%d, %d) = %d, halving gives %d", w, h, got, count)
			}
		}

		// Up to 69 levels: more than any chain has, even from a 2^62-wide base.
		n := int(n8) % 70
		valid := n > 0 && n <= count
		levels := make([]image.Image, n)
		for i := range levels {
			lw, lh := levelSize(w, h, i)
			// Optionally give one level below the base the wrong size.
			if i > 0 && i == 1+int(bad)%(n-1) && (dw != 0 || dh != 0) {
				lw, lh = lw+int(dw), lh+int(dh)
				valid = false
			}
			levels[i] = level(x0+i, y0-i, lw, lh)
		}

		gw, gh, err = validateLevels("fuzz", levels)
		if (err == nil) != valid {
			t.Fatalf("validateLevels(%d levels from %dx%d, full chain %d) = %v, want valid=%v", n, w, h, count, err, valid)
		}
		if err == nil && (gw != w || gh != h) {
			t.Fatalf("validateLevels returned %dx%d for a %dx%d base", gw, gh, w, h)
		}
		if err != nil {
			var wrapped *Error
			if !errors.As(err, &wrapped) || wrapped.Op != "fuzz" {
				t.Fatalf("validateLevels error %#v is not an *Error for the calling operation", err)
			}
		}
	})
}
