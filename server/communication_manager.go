package server

import (
	"fmt"
	"udp/models"
)

func (s *Server) udpWriteWorker(id int) {
	for {
		job := <-s.writeQueue
		_, err := s.conn.WriteToUDP(job.Packet, job.Addr)
		if err != nil {
			fmt.Printf("Writer %d error: %v\n", id, err)
		}
	}
}

func (s *Server) udpReadWorker() {
	buf := make([]byte, 65507)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			fmt.Println("Read error:", err)
			continue
		}
		packet := make([]byte, n)
		copy(packet, buf[:n])
		s.parseQueue <- models.Job{Addr: addr, Packet: packet}
	}
}

func (s *Server) packetSender() {
	for {
		job := <-s.builtpackets
		s.writeQueue <- job
	}
}

// resend (65000)  <-
// resend (10000)  <-
