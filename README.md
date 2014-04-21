gotorrent
=========

BitTorrent client written in Go

##### Usage
```bash
$ ./gotorrent file.torrent[ file2.torrent[ ...]]
```

##### TODO
* Query tracker for more peers after interval
* When downloading the same piece from multiple peers, send cancel to the peers who weren't the fastest
* Store which pieces a peer has to make some operations faster
* Option to seed after completed download
* Show upload/download speed
* Tests
* Text-based UI (ncurses?)
* Request piece from peer when receiving Have if we don't have the piece and peer is idle
