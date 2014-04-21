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
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/marksamman/gotorrent/bencode"
)

type Torrent struct {
	data            map[string]interface{}
	infoHash        []byte
	peers           []*Peer
	handshake       []byte
	files           []File
	pieces          []TorrentPiece
	completedPieces int
	uploadedBytes   uint32

	pieceChannel        chan PieceMessage
	bitfieldChannel     chan BitfieldMessage
	havePieceChannel    chan HavePieceMessage
	addPeerChannel      chan *Peer
	removePeerChannel   chan *Peer
	blockRequestChannel chan BlockRequestMessage
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

type BlockRequestMessage struct {
	from   *Peer
	index  uint32
	begin  uint32
	length uint32
}

func (torrent *Torrent) validatePath(base string, path string) error {
	if absolutePath, err := filepath.Abs(path); err != nil {
		return err
	} else if len(absolutePath) < len(base) {
		return errors.New("path is too short")
	} else if base != absolutePath[:len(base)] {
		return errors.New("path mismatch")
	}
	return nil
}

func (torrent *Torrent) open(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	torrent.data, err = bencode.Decode(file)
	if err != nil {
		return err
	}

	info := torrent.getInfo()

	// Set info hash
	hasher := sha1.New()
	hasher.Write(bencode.Encode(info))
	torrent.infoHash = hasher.Sum(nil)

	// Set handshake
	var buffer bytes.Buffer
	buffer.WriteByte(19) // length of the string "BitTorrent Protocol"
	buffer.WriteString("BitTorrent protocol")
	buffer.WriteString("\x00\x00\x00\x00\x00\x00\x00\x00") // reserved
	buffer.Write(torrent.infoHash)
	buffer.Write(client.peerID)
	torrent.handshake = buffer.Bytes()

	// Set pieces
	pieces := info["pieces"].(string)
	for i := 0; i < len(pieces); i += 20 {
		torrent.pieces = append(torrent.pieces, TorrentPiece{[]*Peer{}, pieces[i : i+20], false, false})
	}

	os.Mkdir("Downloads", 0700)
	if err := os.Chdir("Downloads"); err != nil {
		return err
	}
	defer os.Chdir("..")

	base, err := os.Getwd()
	if err != nil {
		return err
	}

	// Set files
	if files, exists := info["files"]; exists {
		dirName := info["name"].(string)
		if err := torrent.validatePath(base, dirName); err != nil {
			return err
		}

		os.Mkdir(dirName, 0700)
		if err := os.Chdir(dirName); err != nil {
			return err
		}
		defer os.Chdir("..")

		if base, err = os.Getwd(); err != nil {
			return err
		}

		// Multiple files
		var begin int64
		for _, v := range files.([]interface{}) {
			v := v.(map[string]interface{})

			// Set up directory structure
			pathList := v["path"].([]interface{})
			pathElements := []string{}
			for i := 0; i < len(pathList)-1; i++ {
				pathElements = append(pathElements, pathList[i].(string))
			}

			path := filepath.Join(pathElements...)
			fullPath := filepath.Join(path, pathList[len(pathList)-1].(string))
			if err := torrent.validatePath(base, fullPath); err != nil {
				return err
			}

			if len(path) != 0 {
				if err := os.MkdirAll(path, 0700); err != nil {
					return err
				}
			}

			file, err := os.Create(fullPath)
			if err != nil {
				return err
			}

			length := v["length"].(int64)
			torrent.files = append(torrent.files, File{file, begin, length})
			begin += length
		}
	} else {
		// Single file
		fileName := info["name"].(string)
		if err := torrent.validatePath(base, fileName); err != nil {
			return err
		}

		file, err := os.Create(fileName)
		if err != nil {
			return err
		}

		torrent.files = []File{{file, 0, info["length"].(int64)}}
	}
	return nil
}

func (torrent *Torrent) download() error {
	params := make(map[string]string)
	params["event"] = "started"
	resp, err := torrent.sendTrackerRequest(params)
	if err != nil {
		return err
	}

	torrent.pieceChannel = make(chan PieceMessage)
	torrent.bitfieldChannel = make(chan BitfieldMessage)
	torrent.havePieceChannel = make(chan HavePieceMessage)
	torrent.addPeerChannel = make(chan *Peer)
	torrent.removePeerChannel = make(chan *Peer)

	torrent.connectToPeers(resp["peers"])

	for len(torrent.peers) != 0 || torrent.completedPieces != len(torrent.pieces) {
		select {
		case havePieceMessage := <-torrent.havePieceChannel:
			torrent.handleHaveMessage(&havePieceMessage)
		case bitfieldMessage := <-torrent.bitfieldChannel:
			torrent.handleBitfieldMessage(&bitfieldMessage)
		case pieceMessage := <-torrent.pieceChannel:
			torrent.handlePieceMessage(&pieceMessage)
		case blockRequestMessage := <-torrent.blockRequestChannel:
			torrent.handleBlockRequestMessage(&blockRequestMessage)
		case peer := <-torrent.addPeerChannel:
			torrent.handleAddPeer(peer)
		case peer := <-torrent.removePeerChannel:
			torrent.handleRemovePeer(peer)
		}
	}

	params["event"] = "stopped"
	torrent.sendTrackerRequest(params)
	return nil
}

func (torrent *Torrent) checkPieceHash(pieceMessage *PieceMessage) bool {
	hasher := sha1.New()
	hasher.Write(pieceMessage.data)
	return bytes.Equal(hasher.Sum(nil), []byte(torrent.pieces[pieceMessage.index].hash))
}

func (torrent *Torrent) sendTrackerRequest(params map[string]string) (map[string]interface{}, error) {
	var paramBuf bytes.Buffer
	for k, v := range params {
		paramBuf.WriteString(k)
		paramBuf.WriteByte('=')
		paramBuf.WriteString(v)
		paramBuf.WriteByte('&')
	}

	downloadedBytes := torrent.getDownloadedSize()
	httpResponse, err := http.Get(fmt.Sprintf("%s?%speer_id=%s&info_hash=%s&left=%d&compact=1&downloaded=%d&uploaded=%d&port=6881",
		torrent.getAnnounceURL(), paramBuf.String(),
		url.QueryEscape(string(client.peerID)),
		url.QueryEscape(string(torrent.infoHash)),
		torrent.getTotalSize()-downloadedBytes,
		downloadedBytes, torrent.uploadedBytes))
	if err != nil {
		return nil, err
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != 200 {
		return nil, fmt.Errorf("bad response from tracker: %s", httpResponse.Status)
	}

	resp, err := bencode.Decode(httpResponse.Body)
	if err != nil {
		return nil, err
	}

	if failureReason, exists := resp["failure reason"]; exists {
		return nil, errors.New(failureReason.(string))
	}
	return resp, nil
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
	if comment, exists := torrent.data["comment"]; exists {
		return comment.(string)
	}
	return ""
}

func (torrent *Torrent) getPieceLength(pieceIndex int) int64 {
	pieceLength := torrent.getInfo()["piece length"].(int64)
	if pieceIndex == len(torrent.pieces)-1 {
		if res := torrent.getTotalSize() % pieceLength; res != 0 {
			return res
		}
	}
	return pieceLength
}

func (torrent *Torrent) getDownloadedSize() int64 {
	var downloadedSize int64
	for k := range torrent.pieces {
		if torrent.pieces[k].done {
			downloadedSize += torrent.getPieceLength(k)
		}
	}
	return downloadedSize
}

func (torrent *Torrent) getTotalSize() int64 {
	var size int64
	for k := range torrent.files {
		size += torrent.files[k].length
	}
	return size
}

func (torrent *Torrent) connectToPeers(peers interface{}) {
	switch peers.(type) {
	case string:
		buf := bytes.NewBufferString(peers.(string))
		ipBuf := make([]byte, 4)
		for buf.Len() >= 6 {
			peer := NewPeer(torrent)

			// 4 bytes IPv4-address
			buf.Read(ipBuf)
			peer.ip = net.IPv4(ipBuf[0], ipBuf[1], ipBuf[2], ipBuf[3])

			// 2 bytes port
			binary.Read(buf, binary.BigEndian, &peer.port)

			go peer.connect()
		}
	case []interface{}:
		for _, dict := range peers.([]interface{}) {
			dict := dict.(map[string]interface{})

			peer := NewPeer(torrent)
			peer.id = dict["peer id"].(string)
			addr, err := net.ResolveIPAddr("ip", dict["ip"].(string))
			if err != nil {
				continue
			}

			peer.ip = addr.IP
			peer.port = uint16(dict["port"].(int64))
			go peer.connect()
		}
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
	torrent.completedPieces++

	beginPos := int64(pieceMessage.index) * torrent.getPieceLength(0)
	for k := range torrent.files {
		file := &torrent.files[k]
		if beginPos < file.begin {
			break
		}

		if beginPos < file.begin+file.length {
			amountWrite := (file.begin + file.length) - beginPos
			if amountWrite > int64(len(pieceMessage.data)) {
				amountWrite = int64(len(pieceMessage.data))
			}

			file.handle.Seek(beginPos-file.begin, os.SEEK_SET)
			file.handle.Write(pieceMessage.data[:amountWrite])
			pieceMessage.data = pieceMessage.data[amountWrite:]

			beginPos += amountWrite
		}
	}

	for _, peer := range torrent.peers {
		go func(peer *Peer) {
			peer.sendHaveChannel <- pieceMessage.index
		}(peer)
	}

	fmt.Printf("[%s] Downloaded: %.2f%c\n", torrent.getName(), float64(torrent.completedPieces)*100/float64(len(torrent.pieces)), '%')
	if torrent.completedPieces == len(torrent.pieces) {
		for k := range torrent.files {
			torrent.files[k].handle.Close()
		}

		for _, peer := range torrent.peers {
			close(peer.done)
		}

		params := make(map[string]string)
		params["event"] = "completed"
		torrent.sendTrackerRequest(params)
	} else if torrent.completedPieces == len(torrent.pieces)-8 {
		// End game
		incompletePiecesMap := make(map[*Peer][]uint32)
		for k := range torrent.pieces {
			if !torrent.pieces[k].done {
				for _, peer := range torrent.pieces[k].peers {
					incompletePiecesMap[peer] = append(incompletePiecesMap[peer], uint32(k))
				}
			}
		}

		for peer, v := range incompletePiecesMap {
			// shuffle pieces
			for i := 0; i < len(v); i++ {
				j := rand.Intn(i + 1)
				v[i], v[j] = v[j], v[i]
			}

			go func(pieces []uint32, p *Peer) {
				for _, idx := range pieces {
					p.requestPieceChannel <- idx
				}
			}(v, peer)
		}
	} else {
		torrent.requestPieceFromPeer(pieceMessage.from)
	}
}

func (torrent *Torrent) requestPieceFromPeer(peer *Peer) {
	for k := range torrent.pieces {
		if !torrent.pieces[k].busy {
			for _, p := range torrent.peers {
				if p == peer {
					torrent.pieces[k].busy = true
					go func() {
						peer.requestPieceChannel <- uint32(k)
					}()
					return
				}
			}
		}
	}

	// Help with incomplete pieces
	for k := range torrent.pieces {
		if !torrent.pieces[k].done {
			for _, p := range torrent.peers {
				if p == peer {
					go func() {
						peer.requestPieceChannel <- uint32(k)
					}()
					return
				}
			}
		}
	}
}

func (torrent *Torrent) handleAddPeer(peer *Peer) {
	torrent.peers = append(torrent.peers, peer)
	fmt.Printf("[%s] %d active peers\n", torrent.getName(), len(torrent.peers))
}

func (torrent *Torrent) handleRemovePeer(peer *Peer) {
	for idx, v := range torrent.peers {
		if peer == v {
			torrent.peers = append(torrent.peers[:idx], torrent.peers[idx+1:]...)
			break
		}
	}

	for k := range torrent.pieces {
		for idx, v := range torrent.pieces[k].peers {
			if v == peer {
				torrent.pieces[k].peers = append(torrent.pieces[k].peers[:idx], torrent.pieces[k].peers[idx+1:]...)
				break
			}
		}
	}

	for _, v := range peer.pieces {
		if torrent.pieces[v.index].busy && !torrent.pieces[v.index].done {
			torrent.pieces[v.index].busy = false
		}
	}

	fmt.Printf("[%s] %d active peers\n", torrent.getName(), len(torrent.peers))
}

func (torrent *Torrent) handleBlockRequestMessage(blockRequestMessage *BlockRequestMessage) {
	if blockRequestMessage.index >= uint32(len(torrent.pieces)) {
		close(blockRequestMessage.from.done)
		return
	}

	piece := &torrent.pieces[blockRequestMessage.index]
	if !piece.done {
		close(blockRequestMessage.from.done)
		return
	}

	end := int64(blockRequestMessage.begin) + int64(blockRequestMessage.length)
	if end >= torrent.getPieceLength(int(blockRequestMessage.index)) {
		close(blockRequestMessage.from.done)
		return
	}

	block := make([]byte, blockRequestMessage.length)
	var pos int64
	fileOffset := int64(blockRequestMessage.index)*torrent.getPieceLength(0) + int64(blockRequestMessage.begin)
	for k := range torrent.files {
		file := &torrent.files[k]
		if fileOffset+pos < file.begin {
			break
		}

		if fileOffset+pos < file.begin+file.length {
			n := (file.begin + file.length) - (fileOffset + pos)
			if n > int64(blockRequestMessage.length)-pos {
				n = int64(blockRequestMessage.length) - pos
			}

			file.handle.Seek((fileOffset+pos)-file.begin, os.SEEK_SET)
			end := pos + n
			for pos < end {
				count, err := file.handle.Read(block[pos:end])
				if err != nil {
					return
				}
				pos += int64(count)
			}
		}
	}

	torrent.uploadedBytes += blockRequestMessage.length
	blockRequestMessage.from.sendPieceBlockChannel <- BlockMessage{
		blockRequestMessage.index,
		blockRequestMessage.begin,
		block,
	}
}
