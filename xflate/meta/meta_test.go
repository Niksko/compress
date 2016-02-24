// Copyright 2015, Joe Tsai. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE.md file.

package meta

import (
	"bytes"
	"compress/flate"
	"encoding/hex"
	"io/ioutil"
	"math/rand"
	"testing"
)

func mustDecodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func testBackwardCompatibility(t *testing.T, b []byte) {
	// Works only on Go 1.5 and above due to a bug in Go's flate implementation.
	// See https://github.com/golang/go/issues/11030.
	//
	// The following const holds a valid compressed string that uses an empty
	// HDistTree to trigger the bug before performing the backwards
	// compatibility test below.
	const emptyDistBlock = "\x05\xc0\x07\x06\x00\x00\x00\x80\x40\x0f\xff\x37\xa0\xca"
	zd := flate.NewReader(bytes.NewReader([]byte(emptyDistBlock)))
	if _, err := ioutil.ReadAll(zd); err != nil {
		t.Fatal("Empty HDistTree bug found in compress/flate, please use Go 1.5 and above")
	}

	// Append last stream block that just contains the string "test\n".
	const rawTestBlock = "\x01\x04\x00\xfb\xfftest"
	zd = flate.NewReader(bytes.NewBuffer([]byte(string(b) + rawTestBlock)))
	got, err := ioutil.ReadAll(zd)
	if err != nil {
		t.Fatalf("unexpected error, ReadAll() = %v", err)
	}
	if want := "test"; string(got) != want {
		t.Fatalf("mismatching output, ReadAll() = %q, want %q", got, want)
	}
}

func TestReverseSearch(t *testing.T) {
	rand := rand.New(rand.NewSource(0))

	// Search random data (not found).
	data := make([]byte, 1<<12) // 4KiB
	rand.Read(data)
	if idx := ReverseSearch(data); idx != -1 {
		t.Errorf("unexpected meta magic: got %d, want %d", idx, -1)
	}

	// Write arbitrary data.
	buf := bytes.NewBuffer(nil)
	mw := NewWriter(buf)
	for i := 0; i < 4096; i++ {
		cnt := rand.Intn(MaxEncBytes)
		rand.Read(data[:cnt])
		mw.Write(data[:cnt])
		mw.encodeBlock(LastMode(rand.Intn(3)))
	}

	// Reverse search all the blocks.
	var numBlks int64
	data = buf.Bytes()
	for len(data) > 0 {
		pos := ReverseSearch(data)
		if pos == -1 {
			break
		}
		data = data[:pos]
		numBlks++
	}
	if numBlks != mw.NumBlocks {
		t.Errorf("mismatching block count: got %d, want %d", numBlks, mw.NumBlocks)
	}
	if len(data) > 0 {
		t.Errorf("unexpected residual data: got %d bytes", len(data))
	}
}

func TestFuzz(t *testing.T) {
	rand := rand.New(rand.NewSource(0))

	bb := bytes.NewBuffer(nil)
	type X struct {
		buf  []byte
		cnt  int
		last LastMode
	}
	wants := []X{}

	// Encode test.
	mw := new(Writer)
	for numBytes := MinRawBytes; numBytes <= MaxRawBytes; numBytes++ {
		numBits := numBytes * 8
		for zeros := 0; zeros <= numBits; zeros++ {
			ones := numBits - zeros
			huffLen, _ := mw.computeHuffLen(zeros, ones)
			if huffLen == 0 && numBytes <= EnsureRawBytes {
				t.Fatalf("could not compute huffLen (zeros: %d, ones: %d)", zeros, ones)
			}
			if huffLen == 0 {
				continue
			}

			var buf []byte
			perm := rand.Perm(numBits)
			for i := 0; i < numBits/8; i++ {
				var b byte
				for j := 0; j < 8; j++ {
					if perm[8*i+j] >= zeros {
						b |= 1 << uint(j)
					}
				}
				buf = append(buf, b)
			}
			for _, l := range []LastMode{LastNil, LastMeta} {
				mw.Reset(bb)
				mw.bufCnt = copy(mw.buf[:], buf)
				mw.buf0s, mw.buf1s = zeros, ones
				if err := mw.encodeBlock(l); err != nil {
					t.Fatalf("unexpected error, encodeBlock() = %v", err)
				}
				cnt := int(mw.OutputOffset)
				wants = append(wants, X{buf, cnt, l})

				// Ensure theoretical limits are upheld.
				if cnt < MinEncBytes {
					t.Fatalf("exceeded minimum theoretical bounds: %d < %d", cnt, MinEncBytes)
				}
				if cnt > MaxEncBytes {
					t.Fatalf("exceeded maximum theoretical bounds: %d < %d", cnt, MaxEncBytes)
				}
			}
		}
	}

	testBackwardCompatibility(t, bb.Bytes())

	// Decode test.
	mr := new(Reader)
	for _, x := range wants {
		mr.Reset(bb)
		if err := mr.decodeBlock(); err != nil {
			t.Fatalf("unexpected error, decodeBlock() = %v", err)
		}

		if !bytes.Equal(mr.buf, x.buf) {
			t.Fatalf("mismatching data:\ngot  %x\nwant %x", mr.buf, x.buf)
		}
		if mr.last != x.last {
			t.Fatalf("mismatching last mode: got %d, want %d", mr.last, x.last)
		}
		if cnt := int(mr.InputOffset); cnt != x.cnt {
			t.Fatalf("mismatching count: got %d, want %d", cnt, x.cnt)
		}
	}
}

func TestRandom(t *testing.T) {
	rand := rand.New(rand.NewSource(0))

	obuf := bytes.NewBuffer(nil)
	ibuf := bytes.NewBuffer(nil)
	mw := NewWriter(obuf)

	// Encode writer test.
	buf := make([]byte, 100)
	for i := 0; i < 1000; i++ {
		cnt := rand.Intn(len(buf))
		rand.Read(buf[:cnt])
		ibuf.Write(buf[:cnt])

		wrCnt, err := mw.Write(buf[:cnt])
		if err != nil {
			t.Fatalf("unexpected error, Write() = %v", err)
		}
		if wrCnt != cnt {
			t.Fatalf("mismatching write count, Write() = %d, want %d", wrCnt, cnt)
		}
		if int(mw.InputOffset) != ibuf.Len() {
			t.Fatalf("mismatching input offset: got %d, want %d", int(mw.InputOffset), ibuf.Len())
		}
		if int(mw.OutputOffset) != obuf.Len() {
			t.Fatalf("mismatching output offset: got %d, want %d", int(mw.OutputOffset), obuf.Len())
		}
	}
	mw.LastMode = LastMeta
	if err := mw.Close(); err != nil {
		t.Fatalf("unexpected error, Close() = %v", err)
	}

	testBackwardCompatibility(t, obuf.Bytes())

	// Meta encoding should be better than 50% on large inputs.
	eff := 100.0 * float64(len(ibuf.Bytes())) / float64(len(obuf.Bytes()))
	if thres := 50.0; eff < thres {
		t.Errorf("efficiency worse than expected: %0.1f%% < %0.1f%%", eff, thres)
	}

	// Decode reader test.
	mr := NewReader(bytes.NewReader(obuf.Bytes()))
	buf, err := ioutil.ReadAll(mr)
	if err != nil {
		t.Errorf("unexpected error, Read() = %v", err)
	}
	if !bytes.Equal(buf, ibuf.Bytes()) {
		t.Errorf("mismatching output, Read()")
	}
	if err := mr.Close(); err != nil {
		t.Errorf("unexpected error, Close() = %v", err)
	}
	if last := mr.LastMode; last != LastMeta {
		t.Errorf("mismatching last mode: got %d, want %d", last, LastMeta)
	}

	// Verify that statistic agree between Reader/Writer.
	if mr.InputOffset != mw.OutputOffset {
		t.Errorf("mismatching input offset: got %d, want %d", mr.InputOffset, mw.InputOffset)
	}
	if mr.OutputOffset != mw.InputOffset {
		t.Errorf("mismatching output offset: got %d, want %d", mr.OutputOffset, mw.OutputOffset)
	}
	if mr.NumBlocks != mw.NumBlocks {
		t.Errorf("mismatching block count: got %d, want %d", mr.NumBlocks, mw.NumBlocks)
	}
}
