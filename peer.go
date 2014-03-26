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
	"fmt"
	"log"
	"net"
)

type Peer struct {
	IP   uint32
	Port uint16

	torrent *Torrent
}

func (peer *Peer) getStringIP() string {
	return fmt.Sprintf("%d.%d.%d.%d",
		peer.IP>>24, (peer.IP>>16)&255, (peer.IP>>8)%255, peer.IP&255)
}

func (peer *Peer) connect() {
	addr := fmt.Sprintf("%s:%d", peer.getStringIP(), peer.Port)
	fmt.Println("connecting to:", addr)

	conn, err := net.Dial("tcp4", addr)
	if err != nil {
		log.Printf("failed to connect to peer: %s\n", err)
		return
	}
	defer conn.Close()

	log.Printf("connected to peer: %s\n", addr)

	// Send handshake
	if _, err := conn.Write(peer.torrent.Handshake); err != nil {
		log.Printf("failed to send handshake to peer: %s\n", err)
		return
	}

	log.Printf("sent handshake to peer: %s\n", addr)
}
