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
	"bufio"
	"log"
	"os"
	"strconv"
)

type Bencode struct {
	reader *bufio.Reader
}

func (bencode *Bencode) readIntUntil(until byte) int {
	res, err := bencode.reader.ReadSlice(until)
	if err != nil {
		log.Fatal(err)
	}

	value, err := strconv.Atoi(string(res[:len(res)-1]))
	if err != nil {
		log.Fatal(err)
	}
	return value
}

func (bencode *Bencode) readInt() int {
	return bencode.readIntUntil('e')
}

type Element struct {
	Value interface{}
}

func (bencode *Bencode) readList() []Element {
	list := []Element{}
	for true {
		ch, err := bencode.reader.ReadByte()
		if err != nil {
			log.Fatal(err)
		}

		switch ch {
		case 'i':
			list = append(list, Element{bencode.readInt()})
		case 'l':
			list = append(list, Element{bencode.readList()})
		case 'd':
			list = append(list, Element{bencode.readDictionary()})
		case 'e':
			return list
		default:
			if err := bencode.reader.UnreadByte(); err != nil {
				log.Fatal(err)
			}

			list = append(list, Element{bencode.readString()})
		}
	}
	return list
}

func (bencode *Bencode) readString() string {
	len := bencode.readIntUntil(':')

	stringBuffer := make([]byte, len)
	n, err := bencode.reader.Read(stringBuffer)
	if err != nil {
		log.Fatal(err)
	}

	if n != len {
		log.Fatal("missing data in string")
	}
	return string(stringBuffer)
}

func (bencode *Bencode) readDictionary() map[string]interface{} {
	dict := make(map[string]interface{})
	for true {
		key := bencode.readString()
		ch, err := bencode.reader.ReadByte()
		if err != nil {
			log.Fatal(err)
		}

		switch ch {
		case 'i':
			dict[key] = bencode.readInt()
		case 'l':
			dict[key] = bencode.readList()
		case 'd':
			dict[key] = bencode.readDictionary()
		default:
			err := bencode.reader.UnreadByte()
			if err != nil {
				log.Fatal(err)
			}

			dict[key] = bencode.readString()
		}

		nextByte, err := bencode.reader.ReadByte()
		if err != nil {
			log.Fatal(err)
		}

		if nextByte == 'e' {
			return dict
		} else if err := bencode.reader.UnreadByte(); err != nil {
			log.Fatal(err)
		}
	}
	return dict
}

func (bencode *Bencode) decode() map[string]interface{} {
	firstByte, err := bencode.reader.ReadByte()
	if err != nil {
		log.Fatal(err)
	}

	if firstByte != 'd' {
		log.Fatal("torrent file must begin with a dictionary")
	}
	return bencode.readDictionary()
}

func NewBencode(file *os.File) *Bencode {
	return &Bencode{bufio.NewReader(file)}
}
