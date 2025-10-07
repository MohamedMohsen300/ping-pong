package main

import (
	"fmt"
	"udp/server"
)

func main() {
	s, err := server.NewServer(":9000")
	if err != nil {
		panic(err)
	}

	fmt.Println("Server running on port 9000...... :)")
	s.Start()
}
