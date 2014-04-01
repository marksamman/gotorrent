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
	"errors"
	"io"
	"strconv"
)

type BencodeDecoder struct {
	bufio.Reader
}

func (decoder *BencodeDecoder) readIntUntil(until byte) (int, error) {
	res, err := decoder.ReadSlice(until)
	if err != nil {
		return -1, err
	}

	value, err := strconv.Atoi(string(res[:len(res)-1]))
	if err != nil {
		return -1, err
	}
	return value, nil
}

func (decoder *BencodeDecoder) readInt() (int, error) {
	return decoder.readIntUntil('e')
}

func (decoder *BencodeDecoder) readList() ([]interface{}, error) {
	var list []interface{}
	for {
		ch, err := decoder.ReadByte()
		if err != nil {
			return nil, err
		}

		var item interface{}
		switch ch {
		case 'i':
			item, err = decoder.readInt()
		case 'l':
			item, err = decoder.readList()
		case 'd':
			item, err = decoder.readDictionary()
		case 'e':
			return list, nil
		default:
			if err := decoder.UnreadByte(); err != nil {
				return nil, err
			}
			item, err = decoder.readString()
		}

		if err != nil {
			return nil, err
		}
		list = append(list, item)
	}
}

func (decoder *BencodeDecoder) readString() (string, error) {
	len, err := decoder.readIntUntil(':')
	if err != nil {
		return "", err
	}

	stringBuffer := make([]byte, len)
	for pos := 0; pos < len; {
		if n, err := decoder.Read(stringBuffer[pos:]); err != nil {
			return "", err
		} else {
			pos += n
		}
	}
	return string(stringBuffer), nil
}

func (decoder *BencodeDecoder) readDictionary() (map[string]interface{}, error) {
	dict := make(map[string]interface{})
	for {
		key, err := decoder.readString()
		if err != nil {
			return nil, err
		}

		ch, err := decoder.ReadByte()
		if err != nil {
			return nil, err
		}

		var item interface{}
		switch ch {
		case 'i':
			item, err = decoder.readInt()
		case 'l':
			item, err = decoder.readList()
		case 'd':
			item, err = decoder.readDictionary()
		default:
			if err := decoder.UnreadByte(); err != nil {
				return nil, err
			}
			item, err = decoder.readString()
		}

		if err != nil {
			return nil, err
		}

		dict[key] = item

		nextByte, err := decoder.ReadByte()
		if err != nil {
			return nil, err
		}

		if nextByte == 'e' {
			return dict, nil
		} else if err := decoder.UnreadByte(); err != nil {
			return nil, err
		}
	}
}

func BencodeDecode(reader io.Reader) (map[string]interface{}, error) {
	decoder := BencodeDecoder{*bufio.NewReader(reader)}

	if firstByte, err := decoder.ReadByte(); err != nil {
		return nil, err
	} else if firstByte != 'd' {
		return nil, errors.New("torrent file must begin with a dictionary")
	}
	return decoder.readDictionary()
}
