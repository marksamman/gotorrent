package main

import (
	"fmt"
	"log"
	"os"
)

func printHelp() {
	fmt.Println("usage: gotorrent file")
}

func main() {
	if len(os.Args) <= 1 {
		printHelp()
		os.Exit(0)
	}

	file, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	file.Close()
}
