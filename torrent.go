package main

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"net/http"
	"net/url"
	"os"
)

type Torrent struct {
	Data     map[string]interface{}
	InfoHash []byte
}

func (torrent *Torrent) open(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	torrent.Data = BencodeDecode(file)
	file.Close()

	hasher := sha1.New()
	hasher.Write(BencodeEncode(torrent.getInfo()))
	torrent.InfoHash = hasher.Sum(nil)
	return nil
}

func (torrent *Torrent) sendTrackerRequest(params map[string]string) (*http.Response, error) {
	var paramBuf bytes.Buffer
	for k, v := range params {
		paramBuf.WriteString(k)
		paramBuf.WriteByte('=')
		paramBuf.WriteString(v)
		paramBuf.WriteByte('&')
	}
	return http.Get(
		fmt.Sprintf("%s?%sinfo_hash=%s&left=%d", torrent.getAnnounceURL(),
			paramBuf.String(), url.QueryEscape(string(torrent.InfoHash)),
			torrent.getTotalSize()-torrent.getDownloadedSize()))
}

func (torrent *Torrent) getAnnounceURL() string {
	return torrent.Data["announce"].(string)
}

func (torrent *Torrent) getName() string {
	return torrent.getInfo()["name"].(string)
}

func (torrent *Torrent) getInfo() map[string]interface{} {
	return torrent.Data["info"].(map[string]interface{})
}

func (torrent *Torrent) getComment() string {
	return torrent.Data["comment"].(string)
}

func (torrent *Torrent) getDownloadedSize() int {
	return 0
}

func (torrent *Torrent) getTotalSize() int {
	size := 0
	for _, v := range torrent.getInfo()["files"].([]Element) {
		elem := v.Value.(map[string]interface{})
		size += elem["length"].(int)
	}
	return size
}
