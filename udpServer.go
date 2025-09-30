package main

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"time"
)

const (
	_register = 1
	_ping     = 2
	_message  = 3
	_ack      = 4
)

type Job struct {
	Addr   *net.UDPAddr
	Packet []byte
}

type pendingPacketsJob struct {
	Job
	LastSend time.Time
}

type Client struct {
	ID   string
	Addr *net.UDPAddr
}

type Mutex struct {
	Action   string
	Addr     *net.UDPAddr
	Id       string
	Packet   []byte
	PacketID uint16
	Reply    chan interface{}
}

type Server struct {
	conn           *net.UDPConn
	clientsByID    map[string]*Client
	clientsByAddr  map[string]*Client
	writeQueue     chan Job
	pendingPackets map[uint16]pendingPacketsJob
	parseQueue     chan Job
	mux            chan Mutex
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
		pendingPackets: make(map[uint16]pendingPacketsJob),
		parseQueue:     make(chan Job, 100),
		mux:            make(chan Mutex, 1000),
	}
	return s, nil
}

//Workers

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
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()

		reply := make(chan interface{})
		s.mux <- Mutex{Action: "getAllPending", Reply: reply}
		pendings := (<-reply).(map[uint16]pendingPacketsJob)

		for packetID, pending := range pendings {
			if now.Sub(pending.LastSend) >= 10*time.Second {
				fmt.Printf("Retransmitting packet %d\n", packetID)

				s.writeQueue <- pending.Job

				pending.LastSend = now
				s.mux <- Mutex{
					Action:   "addPending",
					PacketID: packetID,
					Addr:     pending.Addr,
					Packet:   pending.Packet,
				}
			}
		}
	}
}

func (s *Server) packetParserWorker() {
	for {
		job := <-s.parseQueue
		s.PacketParser(job.Addr, job.Packet)
	}
}

//

func (s *Server) packetGenerator(addr *net.UDPAddr, msgType byte, payload []byte,isRequest bool) {
	packet := make([]byte, 2+2+1+len(payload))

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	packetID := uint16(r.Intn(65535))

	binary.BigEndian.PutUint16(packet[0:2], packetID)

	enc_dec := uint16(0)
	binary.BigEndian.PutUint16(packet[2:4], enc_dec)
	packet[4] = msgType

	copy(packet[5:], payload)

	if isRequest {
		s.mux <- Mutex{Action: "addPending", PacketID: packetID, Addr: addr, Packet: packet}
	}
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
	s.mux <- Mutex{Action: "registration", Addr: addr, Id: id}
	fmt.Println("Registered client:", id, addr)
}

// out pending
func (s *Server) handlePing(addr *net.UDPAddr) {
	reply := make(chan interface{})
	s.mux <- Mutex{Action: "clientByAddr", Addr: addr, Reply: reply}
	client := (<-reply).(*Client)

	if client == nil {
		fmt.Println("Ping from unknown client:", addr)
		return
	}
	fmt.Printf("Ping from %s\n", client.ID)
	s.packetGenerator(addr, _ping, []byte("pong"),false)
}

func (s *Server) handleMessage(addr *net.UDPAddr, payload []byte) {
	reply := make(chan interface{})
	s.mux <- Mutex{Action: "clientByAddr", Addr: addr, Reply: reply}
	client := (<-reply).(*Client)

	if client == nil {
		fmt.Println("Message from unknown client:", addr)
		return
	}

	fmt.Printf("Message from %s: %s\n", client.ID, string(payload))
}

func (s *Server) handleAck(packetID uint16) {
	s.mux <- Mutex{Action: "deletePending", PacketID: packetID}
	fmt.Printf("ACK received for packet %d\n", packetID)
}
// in pending
func (s *Server) MessageFromServerAnyTime() {
	for {
		var send, id, msg string
		_, err := fmt.Scanln(&send, &id, &msg)
		if err != nil {
			fmt.Println("Error reading input:", err)
			continue
		}

		if send == "send" {
			reply := make(chan interface{})
			s.mux <- Mutex{Action: "clientByID", Id: id, Reply: reply}
			client, _ := (<-reply).(*Client)

			if client != nil {
				s.packetGenerator(client.Addr, _message, []byte(msg),true)
			} else {
				fmt.Printf("Client %s not found\n", id)
			}
		} else {
			fmt.Println("Unknown command")
		}
	}
}

func (s *Server) MutexHandleActions() {
	for {
		mu := <-s.mux
		switch mu.Action {
		case "registration":
			client := &Client{ID: mu.Id, Addr: mu.Addr}
			s.clientsByID[mu.Id] = client
			s.clientsByAddr[mu.Addr.String()] = client

		case "clientByAddr":
			c := s.clientsByAddr[mu.Addr.String()]
			mu.Reply <- c

		case "clientByID":
			c := s.clientsByID[mu.Id]
			mu.Reply <- c

		case "addPending":
			s.pendingPackets[mu.PacketID] = pendingPacketsJob{
				Job:      Job{Addr: mu.Addr, Packet: mu.Packet},
				LastSend: time.Now(),
			}

		case "deletePending":
			delete(s.pendingPackets, mu.PacketID)

		case "getAllPending":
			copy := make(map[uint16]pendingPacketsJob)
			for k, v := range s.pendingPackets {
				copy[k] = v
			}
			mu.Reply <- copy
		}
	}
}

func (s *Server) Start() {
	go s.MutexHandleActions()

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
	server, err := NewServer(":9000")
	if err != nil {
		panic(err)
	}

	fmt.Println("Server running on port 9000")
	server.Start()
}

//     <--- conf(.env)
//done <--- use channal for packetParser_With_udpReadWorker)
//done <--- copy job in PacketJob and set (addr,packet,LastTimeSend)
//done <--- mutex
//     <--- try send photo from client to server  (2 MB)
