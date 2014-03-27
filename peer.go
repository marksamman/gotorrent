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
	"net"
)

const (
	Choke = iota
	Unchoke
	Interested
	NotInterested
	Have
	Bitfield
	Request
	Piece
	Cancel
	Port
)

type Peer struct {
	IP        uint32
	Port      uint16
	torrent   *Torrent
	handshake []byte

	pieces     map[string]struct{}
	connection net.Conn
	choked     bool
	interested bool
}

func NewPeer(torrent *Torrent) Peer {
	peer := Peer{}
	peer.torrent = torrent

	peer.choked = true
	peer.interested = false
	return peer
}

func (peer *Peer) getStringIP() string {
	return fmt.Sprintf("%d.%d.%d.%d",
		peer.IP>>24, (peer.IP>>16)&255, (peer.IP>>8)&255, peer.IP&255)
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

func (peer *Peer) connect(pieceChannel chan FilePiece) {
	addr := fmt.Sprintf("%s:%d", peer.getStringIP(), peer.Port)
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
		err := peer.processMessage(pieceChannel)
		if err != nil {
			log.Printf("failed to process message from peer %s: %s\n", addr, err)
			return
		}

		if !peer.choked {
			for k, _ := range peer.torrent.getPiecesSHA1() {
				pieceLength := int64(peer.torrent.getPieceLength())
				var pos int64
				pos = 0
				for pieceLength != 0 {
					req := pieceLength
					if req > 32768 {
						req = 32768
					}

					peer.sendRequest(uint32(k), 0, uint32(req))
					pos += req
					pieceLength -= req
				}
			}
			peer.choked = true
		}
	}
}

func (peer *Peer) processMessage(pieceChannel chan FilePiece) error {
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

	log.Printf("receive message from peer, len: %d, type: %d\n", length, data[0])

	switch data[0] {
	case Choke:
		if length != 1 {
			return errors.New("length of choke packet must be 1")
		}

		peer.choked = true
	case Unchoke:
		if length != 1 {
			return errors.New("length of unchoke packet must be 1")
		}

		peer.choked = false
	case Interested:
		if length != 1 {
			return errors.New("length of interested packet must be 1")
		}

		peer.interested = true
		// TODO: Unchoke peer and send files
	case NotInterested:
		if length != 1 {
			return errors.New("length of not interested packet must be 1")
		}

		peer.interested = false
	case Have:
		if length != 5 {
			return errors.New("length of have packet must be 5")
		}

		stringPiece := string(data[1:])
		_, exists := peer.torrent.Pieces[stringPiece]
		if exists {
			peer.pieces[stringPiece] = struct{}{}
		}
		peer.sendInterested()
	case Bitfield:
		// ignore
		break

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
		filePiece := FilePiece{}
		binary.Read(buf, binary.BigEndian, &filePiece.index)
		binary.Read(buf, binary.BigEndian, &filePiece.begin)
		filePiece.data = data[9:]
		pieceChannel <- filePiece

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
	}
	return nil
}

func (peer *Peer) sendInterested() {
	peer.connection.Write([]byte{0, 0, 0, 1, 2})
}

func (peer *Peer) sendRequest(index, begin, length uint32) {
	var packet bytes.Buffer
	packet.WriteByte(0)
	packet.WriteByte(0)
	packet.WriteByte(0)
	packet.WriteByte(13)
	packet.WriteByte(Request)

	// Index
	packet.WriteByte(byte(index >> 24))
	packet.WriteByte(byte(index >> 16))
	packet.WriteByte(byte(index >> 8))
	packet.WriteByte(byte(index))

	// Begin
	packet.WriteByte(byte(begin >> 24))
	packet.WriteByte(byte(begin >> 16))
	packet.WriteByte(byte(begin >> 8))
	packet.WriteByte(byte(begin))

	// Length
	packet.WriteByte(byte(length >> 24))
	packet.WriteByte(byte(length >> 16))
	packet.WriteByte(byte(length >> 8))
	packet.WriteByte(byte(length))

	peer.connection.Write(packet.Bytes())
}
