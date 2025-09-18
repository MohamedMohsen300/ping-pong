package main

import (
	"fmt"
	"net"
)

func main() {
	listen, err := net.Listen("tcp", "localhost:5000")
	if err != nil {
		panic(err)
	}
	defer listen.Close()
	fmt.Println("TCP Server is listening on Port:5000")

	for {
		// open new session
		conn, err := listen.Accept()
		if err != nil {
			fmt.Println("Error to open new session", err)
			continue
		}
		go handleClient(conn)
	}
}

func handleClient(conn net.Conn) {
	defer conn.Close()

	// receive message
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		println(err)
	}
	
	// send message
	_, err = conn.Write([]byte(string(buf[:n])))
	if err != nil {
		println(err)
	}
}
