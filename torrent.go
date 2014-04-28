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
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/marksamman/bencode"
)

type Torrent struct {
	name        string
	announceURL string
	comment     string
	pieceLength int64
	totalSize   int64

	peers             map[string]*Peer
	handshake         []byte
	files             []File
	pieces            []TorrentPiece
	completedPieces   int
	uploadedBytes     uint32
	pendingFileWrites int

	pieceChannel        chan *PieceMessage
	bitfieldChannel     chan *BitfieldMessage
	havePieceChannel    chan *HavePieceMessage
	addPeerChannel      chan *Peer
	removePeerChannel   chan *Peer
	blockRequestChannel chan *BlockRequestMessage

	fileWriteDone chan struct{}
}

type TorrentPiece struct {
	done     bool
	busyness int
	hash     string
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

	data, err := bencode.Decode(file)
	if err != nil {
		return err
	}

	torrent.announceURL = data["announce"].(string)
	if comment, ok := data["comment"]; ok {
		torrent.comment = comment.(string)
	}

	info := data["info"].(map[string]interface{})
	torrent.name = info["name"].(string)
	torrent.pieceLength = info["piece length"].(int64)

	// Set info hash
	hasher := sha1.New()
	hasher.Write(bencode.Encode(info))

	// Set handshake
	var buffer bytes.Buffer
	buffer.WriteByte(19) // length of the string "BitTorrent Protocol"
	buffer.WriteString("BitTorrent protocol")
	buffer.WriteString("\x00\x00\x00\x00\x00\x00\x00\x00") // reserved
	buffer.Write(hasher.Sum(nil))
	buffer.Write(client.peerID)
	torrent.handshake = buffer.Bytes()

	// Set pieces
	pieces := info["pieces"].(string)
	for i := 0; i < len(pieces); i += 20 {
		torrent.pieces = append(torrent.pieces, TorrentPiece{false, 0, pieces[i : i+20]})
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

		for _, v := range files.([]interface{}) {
			v := v.(map[string]interface{})
			torrent.totalSize += v["length"].(int64)
		}

		// Multiple files
		var begin int64
		for k, v := range files.([]interface{}) {
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

			length := v["length"].(int64)

			file, err := os.OpenFile(fullPath, os.O_RDWR, 0600)
			if err == nil {
				torrent.findCompletedPieces(file, begin, length, k)
			} else if file, err = os.Create(fullPath); err != nil {
				return err
			}

			torrent.files = append(torrent.files, File{file, begin, length})
			begin += length
		}
	} else {
		// Single file
		fileName := info["name"].(string)
		if err := torrent.validatePath(base, fileName); err != nil {
			return err
		}

		length := info["length"].(int64)
		torrent.totalSize = length

		file, err := os.OpenFile(fileName, os.O_RDWR, 0600)
		if err == nil {
			torrent.findCompletedPieces(file, 0, length, 0)
		} else if file, err = os.Create(fileName); err != nil {
			return err
		}
		torrent.files = []File{{file, 0, length}}
	}
	return nil
}

func (torrent *Torrent) findCompletedPieces(file *os.File, begin, length int64, fileIndex int) {
	if fi, err := file.Stat(); err != nil {
		return
	} else if fi.Size() > length {
		file.Truncate(0)
		return
	}

	buf := make([]byte, torrent.pieceLength)

	var pieceIndex uint32
	if begin != 0 {
		pieceIndex = uint32(begin / torrent.pieceLength)
	}

	fileEnd := begin + length
	pos := int64(pieceIndex) * torrent.pieceLength
	pieceLength := torrent.getPieceLength(pieceIndex)
	if pos+pieceLength > fileEnd {
		return
	}

	if pos < begin {
		bufPos := begin - pos
		if _, err := file.Read(buf[bufPos:]); err != nil {
			return
		}

		for bufPos != 0 {
			fileIndex--
			f := torrent.files[fileIndex]

			if bufPos > f.length {
				if n, err := f.handle.Read(buf[bufPos-f.length : bufPos]); err != nil || int64(n) != f.length {
					return
				}
				bufPos -= f.length
			} else {
				if n, err := f.handle.ReadAt(buf[:bufPos], f.length-bufPos); err != nil || int64(n) != bufPos {
					return
				}
				break
			}
		}

		if torrent.checkPieceHash(buf[:pieceLength], pieceIndex) {
			torrent.pieces[pieceIndex].done = true
			torrent.completedPieces++
		}
		pos += pieceLength
		pieceIndex++
	}

	if _, err := file.Seek(pos-begin, os.SEEK_SET); err != nil {
		return
	}

	reader := bufio.NewReaderSize(file, int(pieceLength))
	for pos+torrent.pieceLength <= fileEnd {
		if n, err := reader.Read(buf); err != nil || n != len(buf) {
			return
		}

		if torrent.checkPieceHash(buf, pieceIndex) {
			torrent.pieces[pieceIndex].done = true
			torrent.completedPieces++
		}
		pos += torrent.pieceLength
		pieceIndex++
	}

	if int(pieceIndex) == len(torrent.pieces)-1 {
		pieceLength = torrent.getLastPieceLength()
		if n, err := reader.Read(buf[:pieceLength]); err != nil || int64(n) != pieceLength {
			return
		}

		if torrent.checkPieceHash(buf[:pieceLength], pieceIndex) {
			torrent.pieces[pieceIndex].done = true
			torrent.completedPieces++
		}
	}
}

func (torrent *Torrent) download() error {
	if torrent.completedPieces == len(torrent.pieces) {
		return nil
	}

	params := make(map[string]string)
	params["event"] = "started"
	resp, err := torrent.sendTrackerRequest(params)
	if err != nil {
		return err
	}

	torrent.pieceChannel = make(chan *PieceMessage)
	torrent.bitfieldChannel = make(chan *BitfieldMessage)
	torrent.havePieceChannel = make(chan *HavePieceMessage)
	torrent.addPeerChannel = make(chan *Peer)
	torrent.removePeerChannel = make(chan *Peer)
	torrent.peers = make(map[string]*Peer)
	torrent.fileWriteDone = make(chan struct{})

	torrent.connectToPeers(resp["peers"])

	trackerIntervalTimer := time.Tick(time.Second * time.Duration(resp["interval"].(int64)))
	for torrent.completedPieces != len(torrent.pieces) || len(torrent.peers) != 0 {
		select {
		case havePieceMessage := <-torrent.havePieceChannel:
			torrent.handleHaveMessage(havePieceMessage)
		case bitfieldMessage := <-torrent.bitfieldChannel:
			torrent.handleBitfieldMessage(bitfieldMessage)
		case pieceMessage := <-torrent.pieceChannel:
			torrent.handlePieceMessage(pieceMessage)
		case blockRequestMessage := <-torrent.blockRequestChannel:
			torrent.handleBlockRequestMessage(blockRequestMessage)
		case peer := <-torrent.addPeerChannel:
			torrent.handleAddPeer(peer)
		case peer := <-torrent.removePeerChannel:
			torrent.handleRemovePeer(peer)
		case <-torrent.fileWriteDone:
			torrent.pendingFileWrites--
		case <-trackerIntervalTimer:
			if resp, err := torrent.sendTrackerRequest(nil); err != nil {
				torrent.connectToPeers(resp["peers"])
			}
		}
	}

	for torrent.pendingFileWrites != 0 {
		<-torrent.fileWriteDone
		torrent.pendingFileWrites--
	}

	for k := range torrent.files {
		torrent.files[k].handle.Close()
	}

	params["event"] = "stopped"
	torrent.sendTrackerRequest(params)
	return nil
}

func (torrent *Torrent) checkPieceHash(data []byte, pieceIndex uint32) bool {
	hasher := sha1.New()
	hasher.Write(data)
	return bytes.Equal(hasher.Sum(nil), []byte(torrent.pieces[pieceIndex].hash))
}

func (torrent *Torrent) sendTrackerRequest(params map[string]string) (map[string]interface{}, error) {
	var paramBuf bytes.Buffer
	if params != nil {
		for k, v := range params {
			paramBuf.WriteString(k)
			paramBuf.WriteByte('=')
			paramBuf.WriteString(v)
			paramBuf.WriteByte('&')
		}
	}
	downloadedBytes := torrent.getDownloadedSize()
	httpResponse, err := http.Get(fmt.Sprintf("%s?%speer_id=%s&info_hash=%s&left=%d&compact=1&downloaded=%d&uploaded=%d&port=6881",
		torrent.announceURL, paramBuf.String(),
		url.QueryEscape(string(client.peerID)),
		url.QueryEscape(string(torrent.getInfoHash())),
		torrent.totalSize-downloadedBytes,
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

func (torrent *Torrent) getPieceLength(pieceIndex uint32) int64 {
	if pieceIndex == uint32(len(torrent.pieces))-1 {
		if res := torrent.totalSize % torrent.pieceLength; res != 0 {
			return res
		}
	}
	return torrent.pieceLength
}

func (torrent *Torrent) getLastPieceLength() int64 {
	if res := torrent.totalSize % torrent.pieceLength; res != 0 {
		return res
	}
	return torrent.pieceLength
}

func (torrent *Torrent) getDownloadedSize() int64 {
	var downloadedSize int64
	for k := range torrent.pieces {
		if torrent.pieces[k].done {
			downloadedSize += torrent.getPieceLength(uint32(k))
		}
	}
	return downloadedSize
}

func (torrent *Torrent) connectToPeers(peers interface{}) {
	switch peers.(type) {
	case string:
		peers := peers.(string)
		for i := 0; i < len(peers); i += 6 {
			peer := NewPeer(torrent)
			peer.ip = net.IPv4(peers[i], peers[i+1], peers[i+2], peers[i+3])
			peer.port = binary.BigEndian.Uint16([]byte(peers[i+4:]))
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
	havePieceMessage.from.pieces[havePieceMessage.index] = struct{}{}
	// torrent.requestPieceFromPeer(peer)
}

func (torrent *Torrent) handleBitfieldMessage(bitfieldMessage *BitfieldMessage) {
	peer := bitfieldMessage.from
	piecesLen := uint32(len(torrent.pieces))

	var index uint32
	for i := 0; i < len(bitfieldMessage.data); i++ {
		b := bitfieldMessage.data[i]
		for v := byte(128); v != 0; v >>= 1 {
			if b&v != v {
				index++
				continue
			}

			if index >= piecesLen {
				break
			}

			peer.pieces[index] = struct{}{}
			index++
		}
	}

	torrent.requestPieceFromPeer(peer)
}

func (torrent *Torrent) handlePieceMessage(pieceMessage *PieceMessage) {
	if torrent.pieces[pieceMessage.index].done {
		return
	}

	if !torrent.checkPieceHash(pieceMessage.data, pieceMessage.index) {
		pieceMessage.from.done <- struct{}{}
		return
	}

	torrent.pieces[pieceMessage.index].done = true
	torrent.completedPieces++
	if torrent.completedPieces != len(torrent.pieces) {
		torrent.requestPieceFromPeer(pieceMessage.from)
	}

	beginPos := int64(pieceMessage.index) * torrent.pieceLength
	for k := range torrent.files {
		file := &torrent.files[k]
		if beginPos < file.begin {
			break
		}

		fileEnd := file.begin + file.length
		if beginPos >= fileEnd {
			continue
		}

		amountWrite := fileEnd - beginPos
		if amountWrite > int64(len(pieceMessage.data)) {
			amountWrite = int64(len(pieceMessage.data))
		}

		torrent.pendingFileWrites++
		go func(handle *os.File, offset int64, data []byte) {
			fileWriterChannel <- &FileWriterMessage{handle, offset, data, torrent}
		}(file.handle, beginPos-file.begin, pieceMessage.data[:amountWrite])
		pieceMessage.data = pieceMessage.data[amountWrite:]
		beginPos += amountWrite
	}

	fmt.Printf("[%s] Downloaded: %.2f%c\n", torrent.name, float64(torrent.completedPieces)*100/float64(len(torrent.pieces)), '%')
	if torrent.completedPieces == len(torrent.pieces) {
		for _, peer := range torrent.peers {
			peer.done <- struct{}{}
		}

		params := make(map[string]string)
		params["event"] = "completed"
		torrent.sendTrackerRequest(params)
	} else {
		for _, peer := range torrent.peers {
			peer.sendHaveChannel <- pieceMessage.index
		}
	}
}

func (torrent *Torrent) requestPieceFromPeer(peer *Peer) {
	// Find the least busy piece that the peer claims to have

	busyness := math.MaxInt32
	var pieceIndex uint32

	pieces := uint32(len(torrent.pieces))
	for i := uint32(0); i < pieces; i++ {
		piece := &torrent.pieces[i]
		if piece.done {
			continue
		}

		if _, ok := peer.pieces[i]; ok {
			if piece.busyness == 0 {
				piece.busyness = 1
				peer.requestPieceChannel <- i
				return
			} else if busyness > piece.busyness {
				busyness = piece.busyness
				pieceIndex = i
			}
		}
	}

	if busyness != math.MaxInt32 {
		torrent.pieces[pieceIndex].busyness++
		peer.requestPieceChannel <- pieceIndex
	}
}

func (torrent *Torrent) handleAddPeer(peer *Peer) {
	torrent.peers[peer.id] = peer
	fmt.Printf("[%s] %d active peers\n", torrent.name, len(torrent.peers))
	if torrent.completedPieces == len(torrent.pieces) {
		peer.done <- struct{}{}
	}
}

func (torrent *Torrent) handleRemovePeer(peer *Peer) {
	delete(torrent.peers, peer.id)
	peer.done <- struct{}{}
	fmt.Printf("[%s] %d active peers\n", torrent.name, len(torrent.peers))
}

func (torrent *Torrent) handleBlockRequestMessage(blockRequestMessage *BlockRequestMessage) {
	if blockRequestMessage.index >= uint32(len(torrent.pieces)) {
		blockRequestMessage.from.done <- struct{}{}
		return
	}

	if !torrent.pieces[blockRequestMessage.index].done {
		blockRequestMessage.from.done <- struct{}{}
		return
	}

	end := int64(blockRequestMessage.begin) + int64(blockRequestMessage.length)
	if end >= torrent.getPieceLength(blockRequestMessage.index) {
		blockRequestMessage.from.done <- struct{}{}
		return
	}

	block := make([]byte, blockRequestMessage.length)
	var pos int64
	fileOffset := int64(blockRequestMessage.index)*torrent.pieceLength + int64(blockRequestMessage.begin)
	for k := range torrent.files {
		file := &torrent.files[k]

		cursor := fileOffset + pos
		if cursor < file.begin {
			break
		}

		fileEnd := file.begin + file.length
		if cursor >= fileEnd {
			continue
		}

		n := fileEnd - cursor
		if n > int64(blockRequestMessage.length)-pos {
			n = int64(blockRequestMessage.length) - pos
		}

		file.handle.Seek(cursor-file.begin, os.SEEK_SET)
		end := pos + n
		for pos < end {
			count, err := file.handle.Read(block[pos:end])
			if err != nil {
				return
			}
			pos += int64(count)
		}
	}

	torrent.uploadedBytes += blockRequestMessage.length
	blockRequestMessage.from.sendPieceBlockChannel <- &BlockMessage{
		blockRequestMessage.index,
		blockRequestMessage.begin,
		block,
	}
}

func (torrent *Torrent) getInfoHash() []byte {
	return torrent.handshake[28:48]
}
