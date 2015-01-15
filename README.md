gotorrent [![Build Status](https://travis-ci.org/marksamman/gotorrent.svg?branch=master)](https://travis-ci.org/marksamman/gotorrent)
=========

BitTorrent client written in Go

##### Usage
```bash
$ ./gotorrent file.torrent[ file2.torrent[ ...]]
```

##### TODO
* When downloading the same piece from multiple peers, send cancel to the peers who weren't fast enough
* Show upload/download speed
* Limit upload/download speed
* Text-based UI (ncurses?) / GUI (must still be possible to run as CLI)
* Request piece from peer when receiving Have if we don't have the piece and peer is idle
* Server-side (allow other peers to connect to us)
* Option to seed after completed download
* Implement a choking algorithm to prevent peers from choking us
* More tests!
