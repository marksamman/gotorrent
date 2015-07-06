/*
 * Copyright (c) 2015 Mark Samman <https://github.com/marksamman/gotorrent>
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
	"os"
	"path/filepath"

	"github.com/marksamman/bencode"
)

type Torrent struct {
	totalPeerCount  int
	files           []File
	pieces          []TorrentPiece
	activePeers     []*Peer
	completedPieces int
	knownPeers      map[string]struct{}

	name        string
	comment     string
	pieceLength int64
	totalSize   int64

	pieceChannel               chan *PieceMessage
	bitfieldChannel            chan *BitfieldMessage
	havePieceChannel           chan *HavePieceMessage
	addPeerChannel             chan *Peer
	removePeerChannel          chan *Peer
	blockRequestChannel        chan *BlockRequestMessage
	activeTrackerChannel       chan int
	stoppedTrackerChannel      chan int
	requestAnnounceDataChannel chan int
	peersChannel               chan interface{}

	fileWriteDone      chan struct{}
	decrementPeerCount chan struct{}

	handshake         []byte
	uploaded          uint64
	pendingFileWrites int

	trackers       []*Tracker
	activeTrackers []int
}

type TorrentPiece struct {
	done     bool
	busyness int
	hash     string
}

type File struct {
	path   string
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

func (torrent *Torrent) parseTrackerURL(url string) error {
	if len(url) < 7 {
		return errors.New("announce URL is too short")
	}

	if url[0:4] == "http" {
		torrent.trackers = append(torrent.trackers, &Tracker{
			id:          len(torrent.trackers),
			announceURL: url,
			protocol:    TrackerHTTP,
			torrent:     torrent,
		})
	} else if url[0:3] == "udp" {
		torrent.trackers = append(torrent.trackers, &Tracker{
			id:          len(torrent.trackers),
			announceURL: url,
			protocol:    TrackerUDP,
			torrent:     torrent,
		})
	} else {
		return errors.New("unsupported tracker protocol")
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

	if announceLists, ok := data["announce-list"].([]interface{}); ok {
		for _, announceList := range announceLists {
			for _, announceURL := range announceList.([]interface{}) {
				torrent.parseTrackerURL(announceURL.(string))
			}
		}
	} else {
		torrent.parseTrackerURL(data["announce"].(string))
	}

	if comment, ok := data["comment"]; ok {
		torrent.comment = comment.(string)
	}

	info := data["info"].(map[string]interface{})
	torrent.name = info["name"].(string)
	torrent.pieceLength = info["piece length"].(int64)

	infoHash := sha1.Sum(bencode.Encode(info))

	// Set handshake
	var buffer bytes.Buffer
	buffer.WriteByte(19) // length of the string "BitTorrent Protocol"
	buffer.WriteString("BitTorrent protocol")
	buffer.WriteString("\x00\x00\x00\x00\x00\x00\x00\x00") // reserved
	buffer.Write(infoHash[:])
	buffer.Write(client.peerID)
	torrent.handshake = buffer.Bytes()

	// Set pieces
	pieces := info["pieces"].(string)
	for i := 0; i < len(pieces); i += 20 {
		torrent.pieces = append(torrent.pieces, TorrentPiece{
			hash: pieces[i : i+20],
		})
	}

	if err := os.Mkdir("Downloads", 0700); err != nil && !os.IsExist(err) {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	base := filepath.Join(cwd, "Downloads")

	// Set files
	if files, exists := info["files"]; exists {
		dirName := filepath.Join("Downloads", info["name"].(string))
		if err := torrent.validatePath(base, dirName); err != nil {
			return err
		}

		base := filepath.Join(cwd, dirName)

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
			pathElements := []string{dirName}
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
				file.Close()
			}

			torrent.files = append(torrent.files, File{fullPath, begin, length})
			begin += length
		}
	} else {
		// Single file
		fileName := filepath.Join("Downloads", info["name"].(string))
		if err := torrent.validatePath(base, fileName); err != nil {
			return err
		}

		length := info["length"].(int64)
		torrent.totalSize = length

		file, err := os.OpenFile(fileName, os.O_RDWR, 0600)
		if err == nil {
			torrent.findCompletedPieces(file, 0, length, 0)
			file.Close()
		}
		torrent.files = []File{{fileName, 0, length}}
	}
	return nil
}

func (torrent *Torrent) findCompletedPieces(file *os.File, begin, length int64, fileIndex int) {
	fi, err := file.Stat()
	if err != nil {
		return
	}

	size := fi.Size()
	if size == 0 {
		return
	} else if size > length {
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

			handle, err := os.OpenFile(f.path, os.O_RDONLY, 0600)
			if err != nil {
				return
			}
			defer handle.Close()

			if bufPos > f.length {
				if n, err := handle.Read(buf[bufPos-f.length : bufPos]); err != nil || int64(n) != f.length {
					return
				}
				bufPos -= f.length
			} else {
				if n, err := handle.ReadAt(buf[:bufPos], f.length-bufPos); err != nil || int64(n) != bufPos {
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

func (torrent *Torrent) getTrackerRequestData(event uint32) *TrackerRequestData {
	downloaded := torrent.getDownloadedSize()
	return &TrackerRequestData{
		event:      event,
		downloaded: uint64(downloaded),
		uploaded:   torrent.uploaded,
		remaining:  uint64(torrent.totalSize - downloaded),
	}
}

func (torrent *Torrent) startTrackers() {
	data := torrent.getTrackerRequestData(TrackerEventStarted)
	for _, tracker := range torrent.trackers {
		go tracker.start(data)
	}
}

func (torrent *Torrent) stopTrackers() {
	data := torrent.getTrackerRequestData(TrackerEventStopped)
	for _, trackerID := range torrent.activeTrackers {
		go func(announceChannel chan *TrackerRequestData, stopChannel chan struct{}) {
			announceChannel <- data
			stopChannel <- struct{}{}
		}(torrent.trackers[trackerID].announceChannel, torrent.trackers[trackerID].stopChannel)
	}
}

func (torrent *Torrent) announceToTrackers(event uint32) {
	data := torrent.getTrackerRequestData(event)
	for _, trackerID := range torrent.activeTrackers {
		go func(channel chan *TrackerRequestData) {
			channel <- data
		}(torrent.trackers[trackerID].announceChannel)
	}
}

func (torrent *Torrent) download() {
	if torrent.completedPieces == len(torrent.pieces) {
		return
	}

	torrent.activeTrackerChannel = make(chan int)
	torrent.stoppedTrackerChannel = make(chan int)
	torrent.requestAnnounceDataChannel = make(chan int)
	torrent.peersChannel = make(chan interface{})
	torrent.startTrackers()

	torrent.pieceChannel = make(chan *PieceMessage)
	torrent.bitfieldChannel = make(chan *BitfieldMessage)
	torrent.havePieceChannel = make(chan *HavePieceMessage)
	torrent.addPeerChannel = make(chan *Peer)
	torrent.removePeerChannel = make(chan *Peer)
	torrent.blockRequestChannel = make(chan *BlockRequestMessage)
	torrent.fileWriteDone = make(chan struct{})
	torrent.decrementPeerCount = make(chan struct{})

	torrent.knownPeers = make(map[string]struct{})

	for torrent.completedPieces != len(torrent.pieces) || torrent.totalPeerCount != 0 {
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
		case <-torrent.decrementPeerCount:
			torrent.totalPeerCount--
		case peers := <-torrent.peersChannel:
			torrent.connectToPeers(peers)
		case trackerID := <-torrent.activeTrackerChannel:
			torrent.activeTrackers = append(torrent.activeTrackers, trackerID)
			fmt.Printf("[%s] %d active trackers\n", torrent.name, len(torrent.activeTrackers))
		case trackerID := <-torrent.requestAnnounceDataChannel:
			go func(channel chan *TrackerRequestData, data *TrackerRequestData) {
				channel <- data
			}(torrent.trackers[trackerID].announceChannel, torrent.getTrackerRequestData(TrackerEventNone))
		}
	}
	torrent.stopTrackers()

	if torrent.pendingFileWrites != 0 {
		fmt.Printf("[%s] Waiting for %d pending file writes...\n", torrent.name, torrent.pendingFileWrites)
		for torrent.pendingFileWrites != 0 {
			<-torrent.fileWriteDone
			torrent.pendingFileWrites--
		}
	}

	if len(torrent.activeTrackers) != 0 {
		fmt.Printf("[%s] Waiting for %d trackers to stop...\n", torrent.name, len(torrent.activeTrackers))
		for len(torrent.activeTrackers) != 0 {
			select {
			case trackerID := <-torrent.stoppedTrackerChannel:
				for k, v := range torrent.activeTrackers {
					if v == trackerID {
						torrent.activeTrackers = append(torrent.activeTrackers[:k], torrent.activeTrackers[k+1:]...)
						break
					}
				}
			// Handle other messages that a Tracker may send
			case <-torrent.activeTrackerChannel:
			case <-torrent.peersChannel:
			case <-torrent.requestAnnounceDataChannel:
			}
		}
	}
}

func (torrent *Torrent) checkPieceHash(data []byte, pieceIndex uint32) bool {
	dataHash := sha1.Sum(data)
	return bytes.Equal(dataHash[:], []byte(torrent.pieces[pieceIndex].hash))
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
	switch peers := peers.(type) {
	case string:
		torrent.totalPeerCount += len(peers) / 6
		for i := 0; i < len(peers); i += 6 {
			peer := NewPeer(torrent)
			peer.ip = net.IPv4(peers[i], peers[i+1], peers[i+2], peers[i+3])
			if _, known := torrent.knownPeers[peer.ip.String()]; known {
				torrent.totalPeerCount--
				continue
			}
			torrent.knownPeers[peer.ip.String()] = struct{}{}

			peer.port = binary.BigEndian.Uint16([]byte(peers[i+4:]))
			go peer.connect()
		}
	case []interface{}:
		torrent.totalPeerCount += len(peers)
		for _, dict := range peers {
			dict := dict.(map[string]interface{})

			addr, err := net.ResolveIPAddr("ip", dict["ip"].(string))
			if err != nil {
				torrent.totalPeerCount--
				continue
			}

			if _, known := torrent.knownPeers[addr.IP.String()]; known {
				torrent.totalPeerCount--
				continue
			}
			torrent.knownPeers[addr.IP.String()] = struct{}{}

			peer := NewPeer(torrent)
			peer.id = dict["peer id"].(string)
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
		go func(path string, offset int64, data []byte) {
			fileWriterChannel <- &FileWriterMessage{path, offset, data, torrent}
		}(file.path, beginPos-file.begin, pieceMessage.data[:amountWrite])
		pieceMessage.data = pieceMessage.data[amountWrite:]
		beginPos += amountWrite
	}

	fmt.Printf("[%s] Downloaded: %.2f%c\n", torrent.name, float64(torrent.completedPieces)*100/float64(len(torrent.pieces)), '%')
	if torrent.completedPieces == len(torrent.pieces) {
		for _, peer := range torrent.activePeers {
			peer.done <- struct{}{}
		}
		torrent.announceToTrackers(TrackerEventCompleted)
	} else {
		for _, peer := range torrent.activePeers {
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
	torrent.activePeers = append(torrent.activePeers, peer)
	fmt.Printf("[%s] %d active peers\n", torrent.name, len(torrent.activePeers))
	if torrent.completedPieces == len(torrent.pieces) {
		peer.done <- struct{}{}
	}
}

func (torrent *Torrent) handleRemovePeer(peer *Peer) {
	for k, p := range torrent.activePeers {
		if p == peer {
			torrent.activePeers = append(torrent.activePeers[:k], torrent.activePeers[k+1:]...)
			break
		}
	}

	peer.done <- struct{}{}
	fmt.Printf("[%s] %d active peers\n", torrent.name, len(torrent.activePeers))
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
	if end > torrent.getPieceLength(blockRequestMessage.index) {
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

		handle, err := os.OpenFile(file.path, os.O_RDONLY, 0600)
		if err != nil {
			return
		}

		handle.Seek(cursor-file.begin, os.SEEK_SET)
		end := pos + n
		for pos < end {
			count, err := handle.Read(block[pos:end])
			if err != nil {
				handle.Close()
				return
			}
			pos += int64(count)
		}
		handle.Close()
	}

	torrent.uploaded += uint64(blockRequestMessage.length)
	blockRequestMessage.from.sendPieceBlockChannel <- &BlockMessage{
		blockRequestMessage.index,
		blockRequestMessage.begin,
		block,
	}
}

func (torrent *Torrent) getInfoHash() []byte {
	return torrent.handshake[28:48]
}
