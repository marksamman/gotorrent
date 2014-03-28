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
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
)

type Torrent struct {
	Data      map[string]interface{}
	InfoHash  []byte
	Peers     []Peer
	Handshake []byte
	Pieces    []TorrentPiece

	checkInterest    chan uint32
	interestResponse chan bool
	pieceChannel     chan FilePiece
}

type TorrentPiece struct {
	hash string
	busy bool
	done bool
}

type FilePiece struct {
	index uint32
	data  []byte
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

	pieces := torrent.getInfo()["pieces"].(string)
	for i := 0; i < len(pieces); i += 20 {
		torrent.Pieces = append(torrent.Pieces, TorrentPiece{pieces[i : i+20], false, false})
	}

	return nil
}

func (torrent *Torrent) download() {
	file, err := os.Create(torrent.getInfo()["name"].(string))
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	torrent.pieceChannel = make(chan FilePiece)
	torrent.checkInterest = make(chan uint32)
	torrent.interestResponse = make(chan bool)
	for _, peer := range torrent.Peers {
		go func(peer Peer) {
			peer.connect()
		}(peer)
	}

	for {
		select {
		case piece := <-torrent.pieceChannel:
			torrent.Pieces[piece.index].done = true

			file.Seek(int64(piece.index)*int64(torrent.getPieceLength(0)), 0)
			file.Write(piece.data)
			file.Sync()

			doneCount := 0
			for _, v := range torrent.Pieces {
				if v.done {
					doneCount++
				}
			}

			fmt.Printf("Downloaded: %.2f%c\n", float64(doneCount)*100/float64(len(torrent.Pieces)), '%')
			if doneCount == len(torrent.Pieces) {
				return
			}

		case pieceIndex := <-torrent.checkInterest:
			if pieceIndex >= uint32(len(torrent.Pieces)) {
				torrent.interestResponse <- false
				break
			}

			if torrent.Pieces[pieceIndex].busy {
				torrent.interestResponse <- false
			} else {
				torrent.interestResponse <- true
				torrent.Pieces[pieceIndex].busy = true
			}
		}
	}
}

func (torrent *Torrent) verifyPiece(piece *FilePiece) bool {
	return true
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

func (torrent *Torrent) getPieceCount() int {
	return len(torrent.Pieces)
}

func (torrent *Torrent) getPieceLength(pieceIndex int) int {
	pieceLength := torrent.getInfo()["piece length"].(int)
	if pieceIndex == len(torrent.Pieces)-1 {
		return int(torrent.getTotalSize() % int64(pieceLength))
	}
	return pieceLength
}

func (torrent *Torrent) getDownloadedSize() int64 {
	var downloadedSize int64
	for k, v := range torrent.Pieces {
		if v.done {
			downloadedSize += int64(torrent.getPieceLength(k))
		}
	}
	return downloadedSize
}

func (torrent *Torrent) getTotalSize() int64 {
	info := torrent.getInfo()
	length, exists := info["length"]
	if !exists {
		// Multiple files
		var size int64
		for _, v := range info["files"].([]interface{}) {
			size += int64(v.(map[string]interface{})["length"].(int))
		}
		return size
	}

	// Single file
	return int64(length.(int))
}

func (torrent *Torrent) parsePeers(peers interface{}) {
	switch peers.(type) {
	case string:
		buf := bytes.NewBufferString(peers.(string))
		ipBuf := make([]byte, 4)
		for buf.Len() >= 6 {
			// 4 bytes ip
			peer := NewPeer(torrent)
			buf.Read(ipBuf)
			peer.IP = net.IPv4(ipBuf[0], ipBuf[1], ipBuf[2], ipBuf[3])
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
