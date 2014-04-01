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
	"io"
	"log"
	"strconv"
)

type BencodeDecoder struct {
	bufio.Reader
}

func (decoder *BencodeDecoder) readIntUntil(until byte) int {
	res, err := decoder.ReadSlice(until)
	if err != nil {
		log.Fatal(err)
	}

	value, err := strconv.Atoi(string(res[:len(res)-1]))
	if err != nil {
		log.Fatal(err)
	}
	return value
}

func (decoder *BencodeDecoder) readInt() int {
	return decoder.readIntUntil('e')
}

func (decoder *BencodeDecoder) readList() []interface{} {
	var list []interface{}
	for {
		ch, err := decoder.ReadByte()
		if err != nil {
			log.Fatal(err)
		}

		switch ch {
		case 'i':
			list = append(list, decoder.readInt())
		case 'l':
			list = append(list, decoder.readList())
		case 'd':
			list = append(list, decoder.readDictionary())
		case 'e':
			return list
		default:
			if err := decoder.UnreadByte(); err != nil {
				log.Fatal(err)
			}

			list = append(list, decoder.readString())
		}
	}
}

func (decoder *BencodeDecoder) readString() string {
	len := decoder.readIntUntil(':')

	stringBuffer := make([]byte, len)
	for pos := 0; pos < len; {
		if n, err := decoder.Read(stringBuffer[pos:]); err != nil {
			log.Fatal(err)
		} else {
			pos += n
		}
	}
	return string(stringBuffer)
}

func (decoder *BencodeDecoder) readDictionary() map[string]interface{} {
	dict := make(map[string]interface{})
	for {
		key := decoder.readString()
		ch, err := decoder.ReadByte()
		if err != nil {
			log.Fatal(err)
		}

		switch ch {
		case 'i':
			dict[key] = decoder.readInt()
		case 'l':
			dict[key] = decoder.readList()
		case 'd':
			dict[key] = decoder.readDictionary()
		default:
			err := decoder.UnreadByte()
			if err != nil {
				log.Fatal(err)
			}

			dict[key] = decoder.readString()
		}

		nextByte, err := decoder.ReadByte()
		if err != nil {
			log.Fatal(err)
		}

		if nextByte == 'e' {
			return dict
		} else if err := decoder.UnreadByte(); err != nil {
			log.Fatal(err)
		}
	}
}

func BencodeDecode(reader io.Reader) map[string]interface{} {
	decoder := BencodeDecoder{*bufio.NewReader(reader)}

	firstByte, err := decoder.ReadByte()
	if err != nil {
		log.Fatal(err)
	}

	if firstByte != 'd' {
		log.Fatal("torrent file must begin with a dictionary")
	}
	return decoder.readDictionary()
}
