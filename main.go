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
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"
)

type Client struct {
	peerID []byte
}

var client Client

func main() {
	if len(os.Args) <= 1 {
		fmt.Println("usage: gotorrent file")
		return
	}

	// Seed rand
	rand.Seed(time.Now().UnixNano())

	// Generate a 20 byte peerId
	var peerID bytes.Buffer
	peerID.WriteString("-GO10000")
	for i := 0; i < 12; i++ {
		peerID.WriteByte(byte(rand.Intn(255)))
	}
	client.peerID = peerID.Bytes()

	// Open torrent file
	torrent := Torrent{}
	if err := torrent.open(os.Args[1]); err != nil {
		log.Fatal(err)
	}

	fmt.Println("Name:", torrent.getName())
	fmt.Println("Announce URL:", torrent.getAnnounceURL())
	fmt.Println("Comment:", torrent.getComment())
	fmt.Printf("Total size: %.2f MB\n", float64(torrent.getTotalSize())/1024/1024)
	if err := torrent.download(); err != nil {
		log.Fatal(err)
	}
}
