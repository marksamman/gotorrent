package bencode

import (
	"bytes"
	"fmt"
	"math"
	"testing"
)

func TestDecodeSinglefileTorrentBencode(t *testing.T) {
	str := "d8:announce41:http://bttracker.debian.org:6969/announce7:comment35:\"Debian CD from cdimage.debian.org\"13:creation datei1391870037e9:httpseedsl85:http://cdimage.debian.org/cdimage/release/7.4.0/iso-cd/debian-7.4.0-amd64-netinst.iso85:http://cdimage.debian.org/cdimage/archive/7.4.0/iso-cd/debian-7.4.0-amd64-netinst.isoe4:infod6:lengthi232783872e4:name30:debian-7.4.0-amd64-netinst.iso12:piece lengthi262144e6:pieces0:ee"
	buf := bytes.NewBufferString(str)
	dict, err := Decode(buf)
	if err != nil {
		t.Error(err)
	}

	if dict["announce"] != "http://bttracker.debian.org:6969/announce" {
		t.Error("announce mismatch")
	} else if dict["comment"] != "\"Debian CD from cdimage.debian.org\"" {
		t.Error("comment mismatch")
	} else if dict["creation date"].(int64) != 1391870037 {
		t.Error("creation date mismatch")
	}

	res := string(Encode(dict))
	if res != str {
		t.Error("mismatch")
	}
}

func TestDecodeListOfInts(t *testing.T) {
	values := []int64{
		math.MinInt8,
		math.MaxUint8,
		math.MinInt16,
		math.MaxUint16,
		math.MinInt32,
		math.MaxUint32,
		math.MinInt64,
		math.MaxInt64,
		-1,
		0,
		1,
	}

	str := fmt.Sprintf("d8:integersli%dei%dei%dei%dei%dei%dei%dei%dei%dei%dei%deee",
		values[0], values[1], values[2], values[3], values[4], values[5],
		values[6], values[7], values[8], values[9], values[10])
	buf := bytes.NewBufferString(str)
	dict, err := Decode(buf)
	if err != nil {
		t.Error(err)
	}

	intList := dict["integers"].([]interface{})
	length := len(intList)
	if length != len(values) {
		t.Error("length mismatch")
	}

	for i := 0; i < length; i++ {
		if intList[i].(int64) != values[i] {
			t.Error("value mismatch at index", i)
		}
	}

	res := string(Encode(dict))
	if res != str {
		t.Error("decode(str).encode != str")
	}
}

func TestDecodeTooLargeInt(t *testing.T) {
	_, err := Decode(bytes.NewBufferString(fmt.Sprintf("d3:keyi%dee", uint64(math.MaxInt64)+1)))
	if err == nil {
		t.Error("expected error")
	}
}
