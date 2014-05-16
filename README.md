gotorrent [![Build Status](https://travis-ci.org/marksamman/gotorrent.svg?branch=master)](https://travis-ci.org/marksamman/gotorrent)
=========

BitTorrent client written in Go

##### Usage
```bash
$ ./gotorrent file.torrent[ file2.torrent[ ...]]
```

##### TODO
* When downloading the same piece from multiple peers, send cancel to the peers who weren't the fastest
* Option to seed after completed download
* Show upload/download speed
* Tests
* Text-based UI (ncurses?)
* Request piece from peer when receiving Have if we don't have the piece and peer is idle
