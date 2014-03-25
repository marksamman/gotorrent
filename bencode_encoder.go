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
	"strconv"
)

type BencodeEncoder struct {
	bytes.Buffer
}

func (encoder *BencodeEncoder) writeString(str string) {
	encoder.WriteString(strconv.Itoa(len(str)))
	encoder.WriteByte(':')
	encoder.WriteString(str)
}

func (encoder *BencodeEncoder) writeInt(v int) {
	encoder.WriteByte('i')
	encoder.WriteString(strconv.Itoa(v))
	encoder.WriteByte('e')
}

func (encoder *BencodeEncoder) writeInterfaceType(v interface{}) {
	switch v.(type) {
	case int:
		encoder.writeInt(v.(int))
	case []Element:
		encoder.writeList(v.([]Element))
	case map[string]interface{}:
		encoder.writeDictionary(v.(map[string]interface{}))
	case string:
		encoder.writeString(v.(string))
	}
}

func (encoder *BencodeEncoder) writeList(list []Element) {
	encoder.WriteByte('l')
	for _, v := range list {
		encoder.writeInterfaceType(v.Value)
	}
	encoder.WriteByte('e')
}

func (encoder *BencodeEncoder) writeDictionary(dict map[string]interface{}) {
	encoder.WriteByte('d')
	for k, v := range dict {
		// Key
		encoder.writeString(k)

		// Value
		encoder.writeInterfaceType(v)
	}
	encoder.WriteByte('e')
}

func BencodeEncode(dict interface{}) []byte {
	encoder := BencodeEncoder{}
	encoder.writeDictionary(dict.(map[string]interface{}))
	return encoder.Bytes()
}
