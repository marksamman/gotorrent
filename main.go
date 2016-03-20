/*
 * Copyright (c) 2016 Mark Samman <https://github.com/marksamman/gotorrent>
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
	"sync"
	"time"
)

type Client struct {
	peerID   []byte
	torrents []*Torrent
}

var client Client
var fileWriterChannel chan *FileWriterMessage

type FileWriterMessage struct {
	filename string
	offset   int64
	data     []byte
	torrent  *Torrent
}

func resendFileWriterMessage(message *FileWriterMessage) {
	fileWriterChannel <- message
}

func fileWriterListener() {
	for {
		message := <-fileWriterChannel

		file, err := os.OpenFile(message.filename, os.O_WRONLY, 0600)
		if err != nil {
			if os.IsNotExist(err) {
				if file, err = os.Create(message.filename); err != nil {
					log.Printf("Failed to create file %s: %s", message.filename, err)
					go resendFileWriterMessage(message)
					continue
				}
			} else {
				log.Printf("Failed to open file %s: %s", message.filename, err)
				go resendFileWriterMessage(message)
				continue
			}
		}

		if _, err := file.WriteAt(message.data, message.offset); err != nil {
			log.Printf("Failed to write to file %s: %s", message.filename, err)
			go resendFileWriterMessage(message)
			continue
		}

		file.Close()

		go func(fileWriteDone chan struct{}) {
			fileWriteDone <- struct{}{}
		}(message.torrent.fileWriteDone)
	}
}

func main() {
	torrentCount := len(os.Args) - 1
	if torrentCount == 0 {
		fmt.Println("usage: gotorrent file.torrent[ file2.torrent[ ...]]")
		return
	}

	// Seed rand
	rand.Seed(time.Now().UnixNano())

	// Generate a 20 byte peerId
	var peerID bytes.Buffer
	peerID.WriteString("-GO10000")
	for i := 0; i < 12; i++ {
		peerID.WriteByte(byte(rand.Intn(256)))
	}
	client.peerID = peerID.Bytes()

	// Open torrent file
	for i := 1; i <= torrentCount; i++ {
		torrent := Torrent{}
		if err := torrent.open(os.Args[i]); err != nil {
			log.Print(err)
			continue
		}

		fmt.Println("Name:", torrent.name)
		fmt.Println("Announce URL:", torrent.trackers[0].announceURL)
		fmt.Println("Comment:", torrent.comment)
		fmt.Printf("Total size: %.2f MB\n", float64(torrent.totalSize/1024/1024))
		fmt.Printf("Downloaded: %.2f MB (%.2f%%)\n", float64(torrent.getDownloadedSize()/1024/1024), float64(torrent.completedPieces)*100/float64(len(torrent.pieces)))
		client.torrents = append(client.torrents, &torrent)
	}

	fileWriterChannel = make(chan *FileWriterMessage)
	go fileWriterListener()

	var group sync.WaitGroup
	group.Add(len(client.torrents))
	for _, torrent := range client.torrents {
		go func(torrent *Torrent) {
			torrent.download()
			group.Done()
		}(torrent)
	}
	group.Wait()
}
