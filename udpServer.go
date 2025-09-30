package main

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"
)

const (
	_register = 1
	_ping     = 2
	_message  = 3
	_ack      = 4
)

type Job struct {
	Addr    *net.UDPAddr
	Packet []byte
}

type Client struct {
	ID   string
	Addr *net.UDPAddr
}

type Server struct {
	conn           *net.UDPConn
	clientsByID    map[string]*Client
	clientsByAddr  map[string]*Client
	mu             sync.Mutex
	writeQueue     chan Job
	pendingPackets map[uint16]Job
	parseQueue     chan Job
}

func NewServer(server string) (*Server, error) {
	addr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return nil, err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}

	s := &Server{
		conn:           conn,
		clientsByID:    make(map[string]*Client),
		clientsByAddr:  make(map[string]*Client),
		writeQueue:     make(chan Job, 100),
		pendingPackets: make(map[uint16]Job),
		parseQueue:     make(chan Job, 100),
	}
	return s, nil
}

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
			fmt.Println("Error reading:", err)
			continue
		}
		packet := make([]byte, n)
		copy(packet, buf[:n])
		s.parseQueue <- Job{Addr: addr, Packet: packet}
	}
}

func (s *Server) fieldPacketTrackingWorker() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		for packetID, job := range s.pendingPackets {
			fmt.Printf("Retransmitting packet %d\n", packetID)
			s.writeQueue <- job
		}
		s.mu.Unlock()
	}
}

func (s *Server) packetParserWorker() {
	for {
		job := <- s.parseQueue
		s.PacketParser(job.Addr, job.Packet)
	}
}

func (s *Server) packetGenerator(addr *net.UDPAddr, msgType byte, payload []byte) {
	packet := make([]byte, 2+2+1+len(payload))

	rand.Seed(time.Now().UnixNano())
	packetID := uint16(rand.Intn(65535))
	binary.BigEndian.PutUint16(packet[0:2], packetID)

	enc_dec := uint16(0)
	binary.BigEndian.PutUint16(packet[2:4], enc_dec)
	packet[4] = msgType

	copy(packet[5:], payload)

	s.pendingPackets[packetID] = Job{Addr: addr, Packet: packet}

	s.writeQueue <- Job{Addr: addr, Packet: packet}
}

func (s *Server) PacketParser(addr *net.UDPAddr, packet []byte) {
	if len(packet) < 5 {
		return
	}

	packetID := binary.BigEndian.Uint16(packet[0:2])
	// enc_dec := binary.BigEndian.Uint16(packet[2:4])
	msgType := packet[4]
	payload := packet[5:]

	switch msgType {
	case _register:
		s.handleRegister(addr, payload)
	case _ping:
		s.handlePing(addr)
	case _message:
		s.handleMessage(addr, payload)
	case _ack:
		s.handleAck(packetID)
	}
}

func (s *Server) handleRegister(addr *net.UDPAddr, payload []byte) {
	id := string(payload)
	client := &Client{ID: id, Addr: addr}
	s.mu.Lock()
	s.clientsByID[id] = client
	s.clientsByAddr[addr.String()] = client
	s.mu.Unlock()
	fmt.Println("Registered client:", id, addr)
}

func (s *Server) handlePing(addr *net.UDPAddr) {
	client, ok := s.clientsByAddr[addr.String()]
	if !ok {
		fmt.Println("Ping from unknown client:", addr)
		return
	}
	fmt.Printf("Ping from %s\n", client.ID)
	s.packetGenerator(addr, _ping, []byte("pong"))
}

func (s *Server) handleMessage(addr *net.UDPAddr, payload []byte) {
	client, ok := s.clientsByAddr[addr.String()]
	if !ok {
		fmt.Println("Message from unknown client:", addr)
		return
	}
	fmt.Printf("Message from %s: %s\n", client.ID, string(payload))
}

func (s *Server) handleAck(packetID uint16) {
	s.mu.Lock()
	delete(s.pendingPackets, packetID)
	s.mu.Unlock()
	fmt.Printf("ACK received for packet %d\n", packetID)
}

func (s *Server) MessageFromServerAnyTime() {
	for {
		var send, id, msg string
		_, err := fmt.Scanln(&send, &id, &msg)
		if err != nil {
			fmt.Println("Error reading input:", err)
			continue
		}

		if send == "send" {
			s.mu.Lock()
			if client, ok := s.clientsByID[id]; ok {
				s.packetGenerator(client.Addr, _message, []byte(msg))
			} else {
				fmt.Printf("Client %s not found\n", id)
			}
			s.mu.Unlock()
		} else {
			fmt.Println("Unknown command")
		}
	}
}

func (s *Server) Start() {
	for i := 1; i <= 3; i++ {
		go s.udpWriteWorker(i)
	}

	go s.udpReadWorker()

	for i := 1; i <= 3; i++ {
		go s.packetParserWorker()
	}

	go s.fieldPacketTrackingWorker()
	go s.MessageFromServerAnyTime()
	select {}
}

func main() {
	server, err := NewServer("173.208.144.109:9000")
	if err != nil {
		panic(err)
	}

	fmt.Println("Server running on port 9000")
	server.Start()
}

// conf -> .env // use channal for packetParser_With_udpReadWorker // copy job in PacketJob and set (addr,packet,LastTimeSend) // mutex // try send photo from client to server  (2 MB)
