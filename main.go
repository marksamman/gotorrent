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
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"time"
)

func printHelp() {
	fmt.Println("usage: gotorrent file")
}

func main() {
	if len(os.Args) <= 1 {
		printHelp()
		os.Exit(0)
	}

	// Open torrent file
	file, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	// Seed rand
	rand.Seed(time.Now().UnixNano())

	// Start a TCP listener on a port in the range 6881-6889
	var port uint16
	port = 6881 + uint16(rand.Intn(8))
	go TCPListener(port)

	// Generate a 20 byte peerId
	var peerId bytes.Buffer
	for i := 0; i < 20; i++ {
		peerId.WriteByte(byte(rand.Intn(255)))
	}

	// Bencode decode the torrent file
	dict := BencodeDecode(file)

	info := dict["info"].(map[string]interface{})
	fmt.Println("Name:", info["name"])
	fmt.Println("Announce URL:", dict["announce"])
	fmt.Println("Comment:", dict["comment"])

	size := 0
	for _, v := range info["files"].([]Element) {
		elem := v.Value.(map[string]interface{})
		size += elem["length"].(int)
	}
	fmt.Printf("Total size: %.2f MB\n", float64(size)/1024/1024)

	hasher := sha1.New()
	hasher.Write(BencodeEncode(dict["info"]))

	url := fmt.Sprintf("%s?info_hash=%s&peer_id=%s&port=%d&left=%d&event=started",
		dict["announce"], url.QueryEscape(string(hasher.Sum(nil))), url.QueryEscape(peerId.String()), port, size)
	fmt.Println("Sending req to:", url)

	res, err := http.Get(url)
	fmt.Printf("Response: %q\n", res)
}
