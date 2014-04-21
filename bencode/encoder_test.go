package bencode

import (
	"fmt"
	"math"
	"testing"
)

func TestEncodeSinglefileTorrentBencode(t *testing.T) {
	dict := make(map[string]interface{})
	dict["announce"] = "http://bttracker.debian.org:6969/announce"
	dict["comment"] = "\"Debian CD from cdimage.debian.org\""
	dict["creation date"] = 1391870037
	dict["httpseeds"] = []interface{}{
		"http://cdimage.debian.org/cdimage/release/7.4.0/iso-cd/debian-7.4.0-amd64-netinst.iso",
		"http://cdimage.debian.org/cdimage/archive/7.4.0/iso-cd/debian-7.4.0-amd64-netinst.iso",
	}

	infoDict := make(map[string]interface{})
	infoDict["length"] = 232783872
	infoDict["name"] = "debian-7.4.0-amd64-netinst.iso"
	infoDict["piece length"] = 262144
	infoDict["pieces"] = ""
	dict["info"] = infoDict

	res := string(Encode(dict))
	expected := "d8:announce41:http://bttracker.debian.org:6969/announce7:comment35:\"Debian CD from cdimage.debian.org\"13:creation datei1391870037e9:httpseedsl85:http://cdimage.debian.org/cdimage/release/7.4.0/iso-cd/debian-7.4.0-amd64-netinst.iso85:http://cdimage.debian.org/cdimage/archive/7.4.0/iso-cd/debian-7.4.0-amd64-netinst.isoe4:infod6:lengthi232783872e4:name30:debian-7.4.0-amd64-netinst.iso12:piece lengthi262144e6:pieces0:ee"
	if res != expected {
		t.Error(fmt.Sprintf("expected %s\ngot %s", expected, res))
	}
}

func TestEncodeListOfInts(t *testing.T) {
	dict := make(map[string]interface{})
	list := []interface{}{}
	list = append(list, int8(math.MinInt8))
	list = append(list, uint8(math.MaxUint8))
	list = append(list, int16(math.MinInt16))
	list = append(list, uint16(math.MaxUint16))
	list = append(list, int32(math.MinInt32))
	list = append(list, uint32(math.MaxUint32))
	list = append(list, int64(math.MinInt64))
	list = append(list, uint64(math.MaxUint64))
	list = append(list, int(-1))
	list = append(list, int(0))
	list = append(list, int(1))
	dict["integers"] = list

	res := string(Encode(dict))
	expected := "d8:integersl"
	expected += fmt.Sprintf("i%de", math.MinInt8)
	expected += fmt.Sprintf("i%de", math.MaxUint8)
	expected += fmt.Sprintf("i%de", math.MinInt16)
	expected += fmt.Sprintf("i%de", math.MaxUint16)
	expected += fmt.Sprintf("i%de", math.MinInt32)
	expected += fmt.Sprintf("i%de", math.MaxUint32)
	expected += fmt.Sprintf("i%de", math.MinInt64)
	expected += fmt.Sprintf("i%de", uint64(math.MaxUint64))
	expected += "i-1e"
	expected += "i0e"
	expected += "i1e"
	expected += "ee"
	if res != expected {
		t.Error(fmt.Sprintf("expected %s\ngot %s", expected, res))
	}
}
