/*
* Copyright (c) 2014 Mark Samman <https://github.com/marksamman/gotorrent>
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

package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/url"
	"os"
)

type Torrent struct {
	Data      map[string]interface{}
	InfoHash  []byte
	Peers     []Peer
	Handshake []byte
	Pieces    map[string]struct{}
}

func (torrent *Torrent) open(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	torrent.Data = BencodeDecode(file)
	file.Close()

	hasher := sha1.New()
	hasher.Write(BencodeEncode(torrent.getInfo()))
	torrent.InfoHash = hasher.Sum(nil)

	var buffer bytes.Buffer
	buffer.WriteByte(19) // length of the string "BitTorrent Protocol"
	buffer.WriteString("BitTorrent protocol")
	buffer.WriteString("\x00\x00\x00\x00\x00\x00\x00\x00") // reserved
	buffer.Write(torrent.InfoHash)
	buffer.Write(client.PeerId)
	torrent.Handshake = buffer.Bytes()

	return nil
}

func (torrent *Torrent) sendTrackerRequest(params map[string]string) (*http.Response, error) {
	var paramBuf bytes.Buffer
	for k, v := range params {
		paramBuf.WriteString(k)
		paramBuf.WriteByte('=')
		paramBuf.WriteString(v)
		paramBuf.WriteByte('&')
	}
	return http.Get(
		fmt.Sprintf("%s?%speer_id=%s&info_hash=%s&left=%d&compact=1",
			torrent.getAnnounceURL(), paramBuf.String(),
			url.QueryEscape(string(client.PeerId)),
			url.QueryEscape(string(torrent.InfoHash)),
			torrent.getTotalSize()-torrent.getDownloadedSize()))
}

func (torrent *Torrent) getAnnounceURL() string {
	return torrent.Data["announce"].(string)
}

func (torrent *Torrent) getName() string {
	return torrent.getInfo()["name"].(string)
}

func (torrent *Torrent) getInfo() map[string]interface{} {
	return torrent.Data["info"].(map[string]interface{})
}

func (torrent *Torrent) getComment() string {
	comment, exists := torrent.Data["comment"]
	if !exists {
		return ""
	}
	return comment.(string)
}

func (torrent *Torrent) getDownloadedSize() int {
	return 0
}

func (torrent *Torrent) getTotalSize() int {
	info := torrent.getInfo()
	length, exists := info["length"]
	if !exists {
		// Multiple files
		size := 0
		for _, v := range info["files"].([]interface{}) {
			size += v.(map[string]interface{})["length"].(int)
		}
		return size
	}

	// Single file
	return length.(int)
}

func (torrent *Torrent) parsePeers(peers interface{}) {
	switch peers.(type) {
	case string:
		buf := bytes.NewBufferString(peers.(string))
		for buf.Len() != 0 {
			// 4 bytes ip
			peer := NewPeer(torrent)
			binary.Read(buf, binary.BigEndian, &peer.IP)
			binary.Read(buf, binary.BigEndian, &peer.Port)
			torrent.Peers = append(torrent.Peers, peer)
		}
	case map[string]interface{}:
		// TODO: dict model
		// peer_id: string
		// ip: hexed ipv6, dotted quad ipv4, dns name string
		// port: int
	}
}
