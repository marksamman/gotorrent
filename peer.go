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
	PieceBlock
	Cancel
	Port
)

type Peer struct {
	ip      net.IP
	port    uint16
	torrent *Torrent

	pieces           []*PeerPiece
	connection       net.Conn
	remoteChoked     bool
	remoteInterested bool
	localChoked      bool
	localInterested  bool

	requestPieceChannel   chan uint32
	sendPieceBlockChannel chan BlockMessage
	sendHaveChannel       chan uint32
	done                  chan struct{}

	id string
}

type PeerPiece struct {
	index     uint32
	data      []byte
	writes    int
	reqWrites int
}

type Packet struct {
	length      uint32
	messageType byte
	payload     []byte
}

type BlockMessage struct {
	index uint32
	begin uint32
	block []byte
}

func NewPeer(torrent *Torrent) *Peer {
	peer := Peer{}
	peer.torrent = torrent

	peer.remoteChoked = true
	peer.remoteInterested = false
	peer.localChoked = true
	peer.localInterested = false
	return &peer
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
	var addr string
	if ip := peer.ip.To4(); ip != nil {
		addr = fmt.Sprintf("%s:%d", ip.String(), peer.port)
	} else {
		addr = fmt.Sprintf("[%s]:%d", peer.ip.String(), peer.port)
	}

	var err error
	if peer.connection, err = net.Dial("tcp", addr); err != nil {
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
	if handshake, err := peer.readN(68); err != nil {
		log.Printf("failed to read handshake from peer: %s\n", err)
		return
	} else if !bytes.Equal(handshake[0:20], []byte("\x13BitTorrent protocol")) {
		log.Printf("bad protocol from peer: %s\n", addr)
		return
	} else if !bytes.Equal(handshake[28:48], peer.torrent.infoHash) {
		log.Printf("info hash mismatch from peer: %s\n", addr)
		return
	} else if len(peer.id) != 0 {
		if !bytes.Equal(handshake[48:68], []byte(peer.id)) {
			log.Printf("peer id mismatch from peer: %s\n", addr)
			return
		}
	} else {
		peer.id = string(handshake[48:68])
	}

	peer.requestPieceChannel = make(chan uint32)
	peer.sendPieceBlockChannel = make(chan BlockMessage)
	peer.sendHaveChannel = make(chan uint32)
	peer.done = make(chan struct{})

	peer.torrent.addPeerChannel <- peer
	defer func() {
		peer.torrent.removePeerChannel <- peer
	}()

	packetChannel := make(chan Packet)
	errorChannel := make(chan error)

	go peer.receiver(packetChannel, errorChannel)
	for {
		select {
		case pieceIndex := <-peer.requestPieceChannel:
			peer.sendPieceRequest(pieceIndex)
		case blockMessage := <-peer.sendPieceBlockChannel:
			peer.sendPieceBlockMessage(&blockMessage)
		case pieceIndex := <-peer.sendHaveChannel:
			peer.sendHaveMessage(pieceIndex)
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

		length := binary.BigEndian.Uint32(lengthHeader)
		if length == 0 {
			// keep-alive
			continue
		}

		data, err := peer.readN(int(length))
		if err != nil {
			errorChannel <- err
			continue
		}

		packetChannel <- Packet{length, data[0], data[1:]}
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
		for _, piece := range peer.pieces {
			peer.requestPiece(piece)
		}
	case Interested:
		if packet.length != 1 {
			return errors.New("length of interested packet must be 1")
		}

		peer.remoteInterested = true
		peer.sendUnchokeMessage()
	case Uninterested:
		if packet.length != 1 {
			return errors.New("length of not interested packet must be 1")
		}

		peer.remoteInterested = false
	case Have:
		if packet.length != 5 {
			return errors.New("length of have packet must be 5")
		}

		index := binary.BigEndian.Uint32(packet.payload)
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

		if !peer.remoteInterested {
			return errors.New("peer sent request without showing interest")
		}

		if peer.localChoked {
			return errors.New("peer sent request while choked")
		}

		index := binary.BigEndian.Uint32(packet.payload)
		begin := binary.BigEndian.Uint32(packet.payload[4:])
		length := binary.BigEndian.Uint32(packet.payload[8:])
		if length > 32768 {
			return errors.New("peer requested length over 32KB")
		}
		peer.torrent.blockRequestChannel <- BlockRequestMessage{peer, index, begin, length}
	case PieceBlock:
		if packet.length < 10 {
			return errors.New("length of piece packet must be at least 10")
		}

		index := binary.BigEndian.Uint32(packet.payload)
		piece, idx := peer.getPeerPiece(index)
		if piece == nil {
			return errors.New("received index we didn't ask for")
		}

		begin := binary.BigEndian.Uint32(packet.payload[4:])
		if int64(begin)+int64(packet.length)-9 > int64(len(piece.data)) {
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

		// TODO: Handle cancel
		/*
		   index := binary.BigEndian.Uint32(packet.payload)
		   begin := binary.BigEndian.Uint32(packet.payload[4:])
		   length := binary.BigEndian.Uint32(packet.payload[8:])
		*/
	case Port:
		if packet.length != 3 {
			return errors.New("length of port packet must be 3")
		}

		// port := binary.BigEndian.Uint16(packet.payload)
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

func (peer *Peer) sendRequest(index, begin, length uint32) {
	packet := make([]byte, 17)
	binary.BigEndian.PutUint32(packet, 13) // Length
	packet[4] = Request
	binary.BigEndian.PutUint32(packet[5:], index)
	binary.BigEndian.PutUint32(packet[9:], begin)
	binary.BigEndian.PutUint32(packet[13:], length)
	peer.connection.Write(packet)
}

func (peer *Peer) getPeerPiece(index uint32) (*PeerPiece, int) {
	for idx, piece := range peer.pieces {
		if piece.index == index {
			return piece, idx
		}
	}
	return nil, -1
}

func (peer *Peer) sendPieceRequest(index uint32) {
	if piece, _ := peer.getPeerPiece(index); piece != nil {
		return
	}

	peer.sendInterested()

	pieceLength := peer.torrent.getPieceLength(index)
	peer.pieces = append(peer.pieces, &PeerPiece{index, make([]byte, pieceLength), 0, int(math.Ceil(float64(pieceLength) / 16384))})
	if !peer.remoteChoked {
		peer.requestPiece(peer.pieces[len(peer.pieces)-1])
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

func (peer *Peer) sendPieceBlockMessage(blockMessage *BlockMessage) {
	packet := make([]byte, 13)
	binary.BigEndian.PutUint32(packet, uint32(9+len(blockMessage.block)))
	packet[4] = PieceBlock
	binary.BigEndian.PutUint32(packet[5:], blockMessage.index)
	binary.BigEndian.PutUint32(packet[9:], blockMessage.begin)
	peer.connection.Write(packet)
	peer.connection.Write(blockMessage.block)
}

func (peer *Peer) sendHaveMessage(pieceIndex uint32) {
	packet := make([]byte, 9)
	packet[3] = 5 // Length
	packet[4] = Have
	binary.BigEndian.PutUint32(packet[5:], pieceIndex)
	peer.connection.Write(packet)
}

func (peer *Peer) sendUnchokeMessage() {
	if !peer.localChoked {
		return
	}

	peer.connection.Write([]byte{0, 0, 0, 1, Unchoke})
	peer.localChoked = false
}
