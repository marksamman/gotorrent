package main

import (
	"math"
	"testing"
	"sync"
)

func TestUpload(t *testing.T) {
	torrent := Torrent{}
	torrent.open("debian.torrent")

	peer := new(Peer)
	peer.torrent = &torrent
	peer.sendPieceBlockChannel = make(chan *BlockMessage)
	peer.done = make(chan struct{})

	messages := 0
	for index := uint32(0); index < uint32(len(torrent.pieces)); index++ {
		if !torrent.pieces[index].done {
			continue
		}
		pieceLength := peer.torrent.getPieceLength(index)
		blocks := int(math.Ceil(float64(pieceLength) / 16384))
		peer.queue = append(peer.queue, &PeerPiece{index, make([][]byte, blocks), 0, blocks})
		messages += blocks
	}

	var group sync.WaitGroup
	group.Add(1)
	go func() {
		for messages != 0 {
			select {
				case message := <-peer.sendPieceBlockChannel:
					func() {
						idx := 0
						var piece *PeerPiece
						for k, v := range peer.queue {
							if v.index == message.index {
								idx = k
								piece = v
								break
							}
						}

						if piece == nil {
							t.Error("received index we didn't ask for")
							return
						}

						blockIndex := message.begin / 16384
						if int(blockIndex) >= len(piece.blocks) {
							t.Error("received too big block index")
							return
						}

						piece.blocks[blockIndex] = message.block

						piece.writes++
						if piece.writes != piece.reqWrites {
							return
						}

						// Glue all blocks together into a piece
						pieceData := []byte{}
						for k := range piece.blocks {
							pieceData = append(pieceData, piece.blocks[k]...)
						}

						// Verify hash
						if !torrent.checkPieceHash(pieceData, message.index) {
							t.Error("piece hash mismatch")
						}

						// Remove piece from peer
						peer.queue = append(peer.queue[:idx], peer.queue[idx+1:]...)
					}()
				case <-peer.done:
					t.Error("handeBlockRequestMessage failed")
			}
			messages--
		}
		group.Done()
	}()

	for index := uint32(0); index < uint32(len(torrent.pieces)); index++ {
		pieceLength := peer.torrent.getPieceLength(index)
		var pos uint32
		for pieceLength > 16384 {
			torrent.handleBlockRequestMessage(&BlockRequestMessage{peer, index, pos, 16384})
			pieceLength -= 16384
			pos += 16384
		}
		torrent.handleBlockRequestMessage(&BlockRequestMessage{peer, index, pos, uint32(pieceLength)})
	}
	group.Wait()
}
