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

func (peer *Peer) connect() {
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
	case 0: // choke
		if length != 1 {
			return errors.New("length of choke packet must be 1")
		}

		peer.choked = true
	case 1: // unchoke
		if length != 1 {
			return errors.New("length of unchoke packet must be 1")
		}

		peer.choked = false
	case 2: // interested
		if length != 1 {
			return errors.New("length of interested packet must be 1")
		}

		peer.interested = true
		// TODO: Unchoke peer and send files
	case 3: // not interested
		if length != 1 {
			return errors.New("length of not interested packet must be 1")
		}

		peer.interested = false
	case 4: // have
		if length != 5 {
			return errors.New("length of have packet must be 5")
		}

		piece, err := peer.readN(4)
		if err != nil {
			return err
		}

		stringPiece := string(piece)
		_, exists := peer.torrent.Pieces[stringPiece]
		if exists {
			peer.pieces[stringPiece] = struct{}{}
		}
	case 5: // bitfield
		// ignore
		break

	case 6: // request
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

	case 7: // piece
		if length < 10 {
			return errors.New("length of piece packet must be at least 10")
		}

		buf := bytes.NewBuffer(data[1:])
		var index, begin uint32
		binary.Read(buf, binary.BigEndian, &index)
		binary.Read(buf, binary.BigEndian, &begin)
		// block := data[9:]

	case 8: // cancel
		if length != 13 {
			return errors.New("length of cancel packet must be 13")
		}

		buf := bytes.NewBuffer(data[1:])
		var index, begin, length uint32
		binary.Read(buf, binary.BigEndian, &index)
		binary.Read(buf, binary.BigEndian, &begin)
		binary.Read(buf, binary.BigEndian, &length)

	case 9: // port
		if length != 3 {
			return errors.New("length of port packet must be 3")
		}

		var port uint16
		binary.Read(bytes.NewBuffer(data[1:]), binary.BigEndian, &port)
	}
	return nil
}
