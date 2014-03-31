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
	ip      net.IP
	port    uint16
	torrent *Torrent

	handshake        []byte
	pieces           []PeerPiece
	connection       net.Conn
	remoteChoked     bool
	remoteInterested bool
	localInterested  bool
	bitfield         []byte

	requestPieceChannel chan uint32
	done                chan struct{}
}

type PeerPiece struct {
	index     uint32
	data      []byte
	writes    int
	reqWrites int
}

type Packet struct {
	length      uint32
	messageType int
	payload     []byte
}

func NewPeer(torrent *Torrent) Peer {
	peer := Peer{}
	peer.torrent = torrent

	peer.remoteChoked = true
	peer.remoteInterested = false
	peer.localInterested = false

	peer.requestPieceChannel = make(chan uint32)
	peer.done = make(chan struct{})
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
	addr := fmt.Sprintf("%s:%d", peer.ip.String(), peer.port)
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
	if _, err := peer.connection.Write(peer.torrent.handshake); err != nil {
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
	if !bytes.Equal(peer.handshake[28:48], peer.torrent.infoHash) {
		log.Printf("info hash mismatch from peer: %s", addr)
		return
	}

	// TODO: Validate peer id if provided by tracker

	log.Printf("successfully exchanged handshake with peer: %s\n", addr)
	packetChannel := make(chan Packet)
	errorChannel := make(chan error)

	go peer.receiver(packetChannel, errorChannel)
	for {
		select {
		case pieceIndex := <-peer.requestPieceChannel:
			peer.sendPieceRequest(pieceIndex)
		case packet := <-packetChannel:
			if err := peer.processMessage(&packet); err != nil {
				log.Printf("error while processing message in peer %s: %s", addr, err)
				return
			}
		case err := <-errorChannel:
			log.Printf("error in peer %s: %s", addr, err)
			return
		case <-peer.done:
			return
		}
	}
}

func (peer *Peer) receiver(packetChannel chan Packet, errorChannel chan error) {
	for {
		lengthHeader, err := peer.readN(4)
		if err != nil {
			errorChannel <- err
			return
		}

		var length uint32
		binary.Read(bytes.NewBuffer(lengthHeader), binary.BigEndian, &length)
		if length == 0 {
			// keep-alive
			continue
		}

		data, err := peer.readN(int(length))
		if err != nil {
			errorChannel <- err
			continue
		}

		packetChannel <- Packet{length, int(data[0]), data[1:]}
	}
}

func (peer *Peer) processMessage(packet *Packet) error {
	switch packet.messageType {
	case Choke:
		if packet.length != 1 {
			return errors.New("length of choke packet must be 1")
		}

		peer.remoteChoked = true
	case Unchoke:
		if packet.length != 1 {
			return errors.New("length of unchoke packet must be 1")
		}

		peer.remoteChoked = false
		for k := range peer.pieces {
			peer.requestPiece(&peer.pieces[k])
		}
	case Interested:
		if packet.length != 1 {
			return errors.New("length of interested packet must be 1")
		}

		peer.remoteInterested = true
		// TODO: Unchoke peer and send files
	case Uninterested:
		if packet.length != 1 {
			return errors.New("length of not interested packet must be 1")
		}

		peer.remoteInterested = false
	case Have:
		if packet.length != 5 {
			return errors.New("length of have packet must be 5")
		}

		var index uint32
		binary.Read(bytes.NewBuffer(packet.payload), binary.BigEndian, &index)
		peer.torrent.havePieceChannel <- HavePieceMessage{peer, index}

	case Bitfield:
		if packet.length < 2 {
			return errors.New("length of bitfield packet must be at least 2")
		}
		peer.torrent.bitfieldChannel <- BitfieldMessage{peer, packet.payload}

	case Request:
		if packet.length != 13 {
			return errors.New("length of request packet must be 13")
		}

		buf := bytes.NewBuffer(packet.payload)
		var index, begin, length uint32
		binary.Read(buf, binary.BigEndian, &index)
		binary.Read(buf, binary.BigEndian, &begin)
		binary.Read(buf, binary.BigEndian, &length)
		if length > 32768 {
			return errors.New("peer requested length over 32KB")
		}

	case Piece:
		if packet.length < 10 {
			return errors.New("length of piece packet must be at least 10")
		}

		var index, begin uint32

		buf := bytes.NewBuffer(packet.payload)
		binary.Read(buf, binary.BigEndian, &index)

		piece, idx := peer.getPeerPiece(index)
		if piece == nil {
			return errors.New("received index we didn't ask for")
		}

		binary.Read(buf, binary.BigEndian, &begin)

		if int(begin+packet.length-9) > len(piece.data) {
			return errors.New("begin+length exceeds length of data buffer")
		}

		copy(piece.data[begin:], packet.payload[8:])
		piece.writes++

		if piece.writes == piece.reqWrites {
			// Send piece to Torrent
			peer.torrent.pieceChannel <- PieceMessage{peer, index, piece.data}

			// Remove piece from peer
			peer.pieces = append(peer.pieces[:idx], peer.pieces[idx+1:]...)
		}

	case Cancel:
		if packet.length != 13 {
			return errors.New("length of cancel packet must be 13")
		}

		buf := bytes.NewBuffer(packet.payload)
		var index, begin, length uint32
		binary.Read(buf, binary.BigEndian, &index)
		binary.Read(buf, binary.BigEndian, &begin)
		binary.Read(buf, binary.BigEndian, &length)
		// TODO: handle cancel

	case Port:
		if packet.length != 3 {
			return errors.New("length of port packet must be 3")
		}

		var port uint16
		binary.Read(bytes.NewBuffer(packet.payload), binary.BigEndian, &port)
		// Peer has a DHT node on port
	}
	return nil
}

func (peer *Peer) sendInterested() {
	if peer.localInterested {
		return
	}

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

func (peer *Peer) getPeerPiece(index uint32) (*PeerPiece, int) {
	for k := range peer.pieces {
		if peer.pieces[k].index == index {
			return &peer.pieces[k], k
		}
	}
	return nil, -1
}

func (peer *Peer) sendPieceRequest(index uint32) {
	peer.sendInterested()

	pieceLength := peer.torrent.getPieceLength(int(index))
	peer.pieces = append(peer.pieces, PeerPiece{index, make([]byte, pieceLength), 0, int(math.Ceil(float64(pieceLength) / 16384))})
	if !peer.remoteChoked {
		peer.requestPiece(&peer.pieces[len(peer.pieces)-1])
	}
}

func (peer *Peer) requestPiece(piece *PeerPiece) {
	var pos uint32
	pieceLength := uint32(len(piece.data))
	for pieceLength > 16384 {
		peer.sendRequest(piece.index, pos, 16384)
		pieceLength -= 16384
		pos += 16384
	}
	peer.sendRequest(piece.index, pos, pieceLength)
}
