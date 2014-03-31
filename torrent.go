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
	data      map[string]interface{}
	infoHash  []byte
	peers     []Peer
	handshake []byte
	files     []File
	pieces    []TorrentPiece

	pieceChannel     chan PieceMessage
	bitfieldChannel  chan BitfieldMessage
	havePieceChannel chan HavePieceMessage
}

type TorrentPiece struct {
	peers []*Peer
	hash  string
	busy  bool
	done  bool
}

type File struct {
	handle *os.File
	begin  int64
	length int64
}

type PieceMessage struct {
	from  *Peer
	index uint32
	data  []byte
}

type BitfieldMessage struct {
	from *Peer
	data []byte
}

type HavePieceMessage struct {
	from  *Peer
	index uint32
}

func (torrent *Torrent) open(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	torrent.data = BencodeDecode(file)
	file.Close()

	info := torrent.getInfo()

	// Set info hash
	hasher := sha1.New()
	hasher.Write(BencodeEncode(info))
	torrent.infoHash = hasher.Sum(nil)

	// Set handshake
	var buffer bytes.Buffer
	buffer.WriteByte(19) // length of the string "BitTorrent Protocol"
	buffer.WriteString("BitTorrent protocol")
	buffer.WriteString("\x00\x00\x00\x00\x00\x00\x00\x00") // reserved
	buffer.Write(torrent.infoHash)
	buffer.Write(client.peerId)
	torrent.handshake = buffer.Bytes()

	// Set pieces
	pieces := info["pieces"].(string)
	for i := 0; i < len(pieces); i += 20 {
		torrent.pieces = append(torrent.pieces, TorrentPiece{[]*Peer{}, pieces[i : i+20], false, false})
	}

	// Set files
	files, exists := info["files"].([]interface{})
	if exists {
		// Multiple files
		var begin int64
		for _, v := range files {
			v := v.(map[string]interface{})

			file, err := os.Create(v["path"].([]interface{})[0].(string))
			if err != nil {
				return err
			}

			length := int64(v["length"].(int))
			torrent.files = append(torrent.files, File{file, begin, length})
			begin += length
		}
	} else {
		// Single file
		file, err := os.Create(info["name"].(string))
		if err != nil {
			return err
		}

		torrent.files = []File{{file, 0, int64(info["length"].(int))}}
	}
	return nil
}

func (torrent *Torrent) download() {
	file, err := os.Create(torrent.getInfo()["name"].(string))
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	torrent.pieceChannel = make(chan PieceMessage)
	torrent.bitfieldChannel = make(chan BitfieldMessage)
	torrent.havePieceChannel = make(chan HavePieceMessage)

	for _, peer := range torrent.peers {
		go func(peer Peer) {
			peer.connect()
		}(peer)
	}

	for {
		select {
		case havePieceMessage := <-torrent.havePieceChannel:
			torrent.handleHaveMessage(&havePieceMessage)

		case bitfieldMessage := <-torrent.bitfieldChannel:
			torrent.handleBitfieldMessage(&bitfieldMessage)

		case pieceMessage := <-torrent.pieceChannel:
			torrent.handlePieceMessage(&pieceMessage)
		}
	}
}

func (torrent *Torrent) checkPieceHash(pieceMessage *PieceMessage) bool {
	hasher := sha1.New()
	hasher.Write(pieceMessage.data)
	return bytes.Equal(hasher.Sum(nil), []byte(torrent.pieces[pieceMessage.index].hash))
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
			url.QueryEscape(string(client.peerId)),
			url.QueryEscape(string(torrent.infoHash)),
			torrent.getTotalSize()-torrent.getDownloadedSize()))
}

func (torrent *Torrent) getAnnounceURL() string {
	return torrent.data["announce"].(string)
}

func (torrent *Torrent) getName() string {
	return torrent.getInfo()["name"].(string)
}

func (torrent *Torrent) getInfo() map[string]interface{} {
	return torrent.data["info"].(map[string]interface{})
}

func (torrent *Torrent) getComment() string {
	comment, exists := torrent.data["comment"]
	if !exists {
		return ""
	}
	return comment.(string)
}

func (torrent *Torrent) getPieceCount() int {
	return len(torrent.pieces)
}

func (torrent *Torrent) getPieceLength(pieceIndex int) int {
	pieceLength := torrent.getInfo()["piece length"].(int)
	if pieceIndex == len(torrent.pieces)-1 {
		return int(torrent.getTotalSize() % int64(pieceLength))
	}
	return pieceLength
}

func (torrent *Torrent) getDownloadedSize() int64 {
	var downloadedSize int64
	for k, v := range torrent.pieces {
		if v.done {
			downloadedSize += int64(torrent.getPieceLength(k))
		}
	}
	return downloadedSize
}

func (torrent *Torrent) getTotalSize() int64 {
	var size int64
	for _, v := range torrent.files {
		size += v.length
	}
	return size
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
			peer.ip = net.IPv4(ipBuf[0], ipBuf[1], ipBuf[2], ipBuf[3])
			binary.Read(buf, binary.BigEndian, &peer.port)
			torrent.peers = append(torrent.peers, peer)
		}
	case map[string]interface{}:
		// TODO: dict model
		// peer_id: string
		// ip: hexed ipv6, dotted quad ipv4, dns name string
		// port: int
	}
}

func (torrent *Torrent) handleHaveMessage(havePieceMessage *HavePieceMessage) {
	index := havePieceMessage.index
	torrent.pieces[index].peers = append(torrent.pieces[index].peers, havePieceMessage.from)

	// torrent.requestPieceFromPeer(havePieceMessage.from)
}

func (torrent *Torrent) handleBitfieldMessage(bitfieldMessage *BitfieldMessage) {
	index := -1
	for i := 0; i < len(bitfieldMessage.data); i++ {
		b := bitfieldMessage.data[i]
		for v := byte(128); v != 0; v >>= 1 {
			index++
			if b&v != v {
				continue
			}

			if index >= len(torrent.pieces) {
				break
			}

			torrent.pieces[index].peers = append(torrent.pieces[index].peers, bitfieldMessage.from)
		}
	}

	torrent.requestPieceFromPeer(bitfieldMessage.from)
}

func (torrent *Torrent) handlePieceMessage(pieceMessage *PieceMessage) {
	if torrent.pieces[pieceMessage.index].done {
		return
	}

	if !torrent.checkPieceHash(pieceMessage) {
		torrent.pieces[pieceMessage.index].busy = false
		close(pieceMessage.from.done)
		return
	}

	torrent.pieces[pieceMessage.index].done = true

	beginPos := int64(pieceMessage.index) * int64(torrent.getPieceLength(0))
	for _, file := range torrent.files {
		if beginPos >= file.begin && beginPos < file.begin+file.length {
			offsetWrite := beginPos - file.begin
			amountWrite := (file.begin + file.length) - beginPos
			if amountWrite > int64(len(pieceMessage.data)) {
				amountWrite = int64(len(pieceMessage.data))
			}
			file.handle.Seek(offsetWrite, 0)
			file.handle.Write(pieceMessage.data[:amountWrite])
			pieceMessage.data = pieceMessage.data[amountWrite:]
		}
	}

	doneCount := 0
	for _, v := range torrent.pieces {
		if v.done {
			doneCount++
		}
	}

	fmt.Printf("Downloaded: %.2f%c\n", float64(doneCount)*100/float64(len(torrent.pieces)), '%')
	if doneCount == len(torrent.pieces) {
		for _, file := range torrent.files {
			file.handle.Close()
		}

		// TODO: Graceful shutdown
		os.Exit(0)
	} else if doneCount == len(torrent.pieces)-8 {
		// End game
		for k, v := range torrent.pieces {
			if !v.done {
				for _, peer := range v.peers {
					go func(idx int, p *Peer) {
						p.requestPieceChannel <- uint32(idx)
					}(k, peer)
				}
			}
		}
	} else {
		torrent.requestPieceFromPeer(pieceMessage.from)
	}
}

func (torrent *Torrent) requestPieceFromPeer(peer *Peer) {
	for k, v := range torrent.pieces {
		if !v.busy {
			torrent.pieces[k].busy = true
			go func() {
				peer.requestPieceChannel <- uint32(k)
			}()
			break
		}
	}
}
