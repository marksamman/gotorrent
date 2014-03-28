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
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"sort"
)

const (
	Choke = iota
	Unchoke
	Interested
	Uninterested
	Have
	Bitfield
	Request
	Piece
	Cancel
	Port
)

type Peer struct {
	IP      net.IP
	Port    uint16
	torrent *Torrent

	handshake        []byte
	pieces           []PeerPiece
	connection       net.Conn
	remoteChoked     bool
	remoteInterested bool
	localInterested  bool
}

type PeerPiece struct {
	blocks    []PieceBlock
	reqBlocks int
}

type PieceBlock struct {
	begin uint32
	data  []byte
}

func (piece *PeerPiece) Len() int {
	return len(piece.blocks)
}

func (piece *PeerPiece) Swap(i, j int) {
	piece.blocks[i], piece.blocks[j] = piece.blocks[j], piece.blocks[i]
}

func (piece *PeerPiece) Less(i, j int) bool {
	return piece.blocks[i].begin < piece.blocks[j].begin
}

func NewPeer(torrent *Torrent) Peer {
	peer := Peer{}
	peer.torrent = torrent

	peer.remoteChoked = true
	peer.remoteInterested = false
	peer.localInterested = false
	peer.pieces = make([]PeerPiece, torrent.getPieceCount())
	for i := 0; i < len(peer.pieces); i++ {
		peer.pieces[i].reqBlocks = int(math.Ceil(float64(torrent.getPieceLength(i)) / 16384))
	}
	return peer
}

func (peer *Peer) readN(n int) ([]byte, error) {
	buf := make([]byte, n)
	for pos := 0; pos < n; {
		count, err := peer.connection.Read(buf[pos:])
		if err != nil {
			return nil, err
		}
		pos += count
	}
	return buf, nil
}

func (peer *Peer) connect() {
	addr := fmt.Sprintf("%s:%d", peer.IP.String(), peer.Port)
	log.Println("connecting to:", addr)

	var err error
	peer.connection, err = net.Dial("tcp4", addr)
	if err != nil {
		log.Printf("failed to connect to peer: %s\n", err)
		return
	}
	defer peer.connection.Close()

	log.Printf("connected to peer: %s\n", addr)

	// Send handshake
	if _, err := peer.connection.Write(peer.torrent.Handshake); err != nil {
		log.Printf("failed to send handshake to peer: %s\n", err)
		return
	}

	// Receive handshake
	peer.handshake, err = peer.readN(68)
	if err != nil {
		log.Printf("failed to read handshake from peer: %s\n", err)
		return
	}

	// Validate info hash
	if !bytes.Equal(peer.handshake[28:48], peer.torrent.InfoHash) {
		log.Printf("info hash mismatch from peer: %s", addr)
		return
	}

	// TODO: Validate peer id if provided by tracker

	log.Printf("successfully exchanged handshake with peer: %s\n", addr)
	for {
		err := peer.processMessage()
		if err != nil {
			log.Printf("failed to process message from peer %s: %s\n", addr, err)
			return
		}
	}
}

func (peer *Peer) processMessage() error {
	lengthHeader, err := peer.readN(4)
	if err != nil {
		return err
	}

	var length uint32
	binary.Read(bytes.NewBuffer(lengthHeader), binary.BigEndian, &length)
	if length == 0 {
		// keep-alive
		return nil
	}

	data, err := peer.readN(int(length))
	if err != nil {
		return err
	}

	switch data[0] {
	case Choke:
		if length != 1 {
			return errors.New("length of choke packet must be 1")
		}

		peer.remoteChoked = true
	case Unchoke:
		if length != 1 {
			return errors.New("length of unchoke packet must be 1")
		}

		peer.remoteChoked = false
	case Interested:
		if length != 1 {
			return errors.New("length of interested packet must be 1")
		}

		peer.remoteInterested = true
		// TODO: Unchoke peer and send files
	case Uninterested:
		if length != 1 {
			return errors.New("length of not interested packet must be 1")
		}

		peer.remoteInterested = false
	case Have:
		if length != 5 {
			return errors.New("length of have packet must be 5")
		}

		var index uint32
		binary.Read(bytes.NewBuffer(data[1:]), binary.BigEndian, &index)

		// Ask Torrent if we want this piece
		peer.torrent.checkInterest <- index
		if <-peer.torrent.interestResponse {
			peer.sendInterested()
			peer.requestPiece(index)
		} else {
			peer.sendUninterested()
		}
	case Bitfield:
		if length < 6 {
			return errors.New("length of bitfield packet must be at least 6")
		}

		buf := bytes.NewBuffer(data[1:])
		var index uint32
		for buf.Len() != 0 {
			var b byte
			if b, err = buf.ReadByte(); err != nil {
				return err
			}

			for i := 128; i != 0; i >>= 1 {
				if b&byte(i) != byte(i) {
					index++
					continue
				}

				// Ask Torrent if we want this piece
				peer.torrent.checkInterest <- index
				if <-peer.torrent.interestResponse {
					peer.sendInterested()
					peer.requestPiece(index)
				} else {
					peer.sendUninterested()
				}
				index++
			}
		}

	case Request:
		if length != 13 {
			return errors.New("length of request packet must be 13")
		}

		buf := bytes.NewBuffer(data[1:])
		var index, begin, length uint32
		binary.Read(buf, binary.BigEndian, &index)
		binary.Read(buf, binary.BigEndian, &begin)
		binary.Read(buf, binary.BigEndian, &length)
		if length > 32768 {
			return errors.New("peer requested length over 32KB")
		}

	case Piece:
		if length < 10 {
			return errors.New("length of piece packet must be at least 10")
		}

		buf := bytes.NewBuffer(data[1:])
		var index uint32
		pieceBlock := PieceBlock{}
		binary.Read(buf, binary.BigEndian, &index)
		binary.Read(buf, binary.BigEndian, &pieceBlock.begin)
		pieceBlock.data = data[9:]

		peer.pieces[index].blocks = append(peer.pieces[index].blocks, pieceBlock)
		if len(peer.pieces[index].blocks) == peer.pieces[index].reqBlocks {
			var pieceBuffer bytes.Buffer
			sort.Sort(&peer.pieces[index])
			for _, v := range peer.pieces[index].blocks {
				pieceBuffer.Write(v.data)
			}
			peer.torrent.pieceChannel <- FilePiece{index, pieceBuffer.Bytes()}
		}

	case Cancel:
		if length != 13 {
			return errors.New("length of cancel packet must be 13")
		}

		buf := bytes.NewBuffer(data[1:])
		var index, begin, length uint32
		binary.Read(buf, binary.BigEndian, &index)
		binary.Read(buf, binary.BigEndian, &begin)
		binary.Read(buf, binary.BigEndian, &length)

	case Port:
		if length != 3 {
			return errors.New("length of port packet must be 3")
		}

		var port uint16
		binary.Read(bytes.NewBuffer(data[1:]), binary.BigEndian, &port)
		// Peer has a DHT node on port
	}
	return nil
}

func (peer *Peer) sendInterested() {
	peer.connection.Write([]byte{0, 0, 0, 1, Interested})
	peer.localInterested = true
}

func (peer *Peer) sendUninterested() {
	peer.connection.Write([]byte{0, 0, 0, 1, Uninterested})
	peer.localInterested = false
}

func (peer *Peer) sendRequest(index, begin, length uint32) {
	var packet bytes.Buffer
	binary.Write(&packet, binary.BigEndian, uint32(13))
	packet.WriteByte(Request)
	binary.Write(&packet, binary.BigEndian, index)
	binary.Write(&packet, binary.BigEndian, begin)
	binary.Write(&packet, binary.BigEndian, length)
	peer.connection.Write(packet.Bytes())
}

func (peer *Peer) requestPiece(index uint32) {
	pieceLength := peer.torrent.getPieceLength(int(index))
	var pos uint32
	for pieceLength > 16384 {
		peer.sendRequest(index, pos, 16384)
		pieceLength -= 16384
		pos += 16384
	}
	peer.sendRequest(index, pos, uint32(pieceLength))
}
