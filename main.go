package main

import (
	"fmt"
	"udp/server"
)

func main() {
	s, err := server.NewServer(":11000")
	if err != nil {
		panic(err)
	}

	fmt.Println("Server running on port 11000...... :)")
	s.Start()
}
