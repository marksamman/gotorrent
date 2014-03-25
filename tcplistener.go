package main

import (
	"fmt"
	"log"
	"net"
)

func TCPListener(port int) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatal(err)
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Print(err)
		}
		go handleConnection(conn)
	}
}

func handleConnection(conn net.Conn) {

}
