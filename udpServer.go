package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	_register = 1
	_ping     = 2
	_message  = 3
	_ack      = 4
	_metadata = 5
	_chunk    = 6
)

type Job struct {
	Addr   *net.UDPAddr
	Packet []byte
}

type GenTask struct {
	Addr              *net.UDPAddr
	MsgType           byte
	Payload           []byte
	ClientAckPacketId uint16
	AckChan           chan struct{}
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
	AckChan  chan struct{}
}

type Server struct {
	conn           *net.UDPConn
	clientsByID    map[string]*Client
	clientsByAddr  map[string]*Client
	writeQueue     chan Job
	pendingPackets map[uint16]pendingPacketsJob
	parseQueue     chan Job
	genQueue       chan GenTask
	mux            chan Mutex
	ackNotify      map[uint16]chan struct{}
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
		writeQueue:     make(chan Job, 1000),
		pendingPackets: make(map[uint16]pendingPacketsJob),
		parseQueue:     make(chan Job, 1000),
		genQueue:       make(chan GenTask, 1000),
		mux:            make(chan Mutex, 1000),
		ackNotify:      make(map[uint16]chan struct{}),
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
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()

		reply := make(chan interface{})
		s.mux <- Mutex{Action: "getAllPending", Reply: reply}
		pendings := (<-reply).(map[uint16]pendingPacketsJob)

		for packetID, pending := range pendings {
			if now.Sub(pending.LastSend) >= 500 * time.Millisecond {
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

func (s *Server) packetGenerator(addr *net.UDPAddr, msgType byte, payload []byte, clientAckPacketId uint16, ackChan chan struct{}) {
	task := GenTask{
		Addr:              addr,
		MsgType:           msgType,
		Payload:           payload,
		ClientAckPacketId: clientAckPacketId,
		AckChan:           ackChan,
	}
	s.genQueue <- task
}

func (s *Server) packetGeneratorWorker() {
	for {
		task := <-s.genQueue
		packet := make([]byte, 2+2+1+len(task.Payload))
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		packetID := uint16(r.Intn(65535))

		binary.BigEndian.PutUint16(packet[2:4], 0)
		packet[4] = task.MsgType
		copy(packet[5:], task.Payload)

		if task.MsgType != _ack {
			binary.BigEndian.PutUint16(packet[0:2], packetID)
			s.mux <- Mutex{Action: "addPending", PacketID: packetID, Addr: task.Addr, Packet: packet}

			if task.AckChan != nil {
				s.mux <- Mutex{Action: "registerAckNotify", PacketID: packetID, AckChan: task.AckChan}
			}
		} else {
			binary.BigEndian.PutUint16(packet[0:2], task.ClientAckPacketId)
		}

		s.writeQueue <- Job{Addr: task.Addr, Packet: packet}
	}
}

func (s *Server) PacketParser(addr *net.UDPAddr, packet []byte) {
	if len(packet) < 5 {
		return
	}

	packetID := binary.BigEndian.Uint16(packet[0:2])
	binary.BigEndian.PutUint16(packet[2:4], 0)
	msgType := packet[4]
	payload := packet[5:]

	switch msgType {
	case _register:
		s.handleRegister(addr, payload, packetID)
	case _ping:
		s.handlePing(addr, packetID)
	case _message:
		s.handleMessage(addr, payload, packetID)
	case _ack:
		s.handleAck(packetID, payload)
	case _metadata:
		fmt.Printf("Received metadata from %s: %s\n", addr.String(), string(payload))
	}
}

func (s *Server) handleRegister(addr *net.UDPAddr, payload []byte, clientAckPacketId uint16) {
	id := string(payload)
	s.mux <- Mutex{Action: "registration", Addr: addr, Id: id}
	s.packetGenerator(addr, _ack, []byte("Registered success"), clientAckPacketId, nil)
	fmt.Println("Registered client:", id, addr)
}

func (s *Server) handlePing(addr *net.UDPAddr, clientAckPacketId uint16) {
	reply := make(chan interface{})
	s.mux <- Mutex{Action: "clientByAddr", Addr: addr, Reply: reply}
	client := (<-reply).(*Client)

	if client == nil {
		fmt.Println("Ping from unknown client:", addr)
		return
	}
	s.packetGenerator(addr, _ack, []byte("pong"), clientAckPacketId, nil)
	fmt.Printf("Ping from %s\n", client.ID)
}

func (s *Server) handleMessage(addr *net.UDPAddr, payload []byte, clientAckPacketId uint16) {
	reply := make(chan interface{})
	s.mux <- Mutex{Action: "clientByAddr", Addr: addr, Reply: reply}
	client := (<-reply).(*Client)

	if client == nil {
		fmt.Println("Message from unknown client:", addr)
		return
	}
	s.packetGenerator(addr, _ack, []byte("message received"), clientAckPacketId, nil)
	fmt.Printf("Message from %s: %s\n", client.ID, string(payload))
}

func (s *Server) handleAck(packetID uint16, payload []byte) {
	fmt.Println("Client ack:", string(payload))
	s.mux <- Mutex{Action: "deletePending", PacketID: packetID}
}

func (s *Server) MessageFromServerAnyTime() {
	for {
		var send, id, msg string
		_, err := fmt.Scanln(&send, &id, &msg)
		if err != nil {
			fmt.Println("Error reading input:", err)
			continue
		}

		reply := make(chan interface{})
		s.mux <- Mutex{Action: "clientByID", Id: id, Reply: reply}
		client, _ := (<-reply).(*Client)
		if client == nil {
			fmt.Printf("Client %s not found\n", id)
			continue
		}

		if send == "send" {
			s.packetGenerator(client.Addr, _message, []byte(msg), 0, nil)
		} else if send == "sendfile" {
			err := s.SendFileToClient(id, msg, filepath.Base(msg), 15, 60000)
			if err != nil {
				fmt.Println("SendFile error:", err)
			}
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
			if ch, ok := s.ackNotify[mu.PacketID]; ok {
				go func(c chan struct{}) {
					close(c)
				}(ch)
				delete(s.ackNotify, mu.PacketID)
			}

		case "getAllPending":
			copy := make(map[uint16]pendingPacketsJob)
			for k, v := range s.pendingPackets {
				copy[k] = v
			}
			mu.Reply <- copy

		case "registerAckNotify":
			if mu.AckChan != nil {
				s.ackNotify[mu.PacketID] = mu.AckChan
			}
		}
	}
}

func (s *Server) SendFileToClient(clientID string, filepath string, filename string, concurrentlyNum int, chunkSize int) error {
	reply := make(chan interface{})
	s.mux <- Mutex{Action: "clientByID", Id: clientID, Reply: reply}
	clientI := (<-reply).(*Client)
	if clientI == nil {
		return fmt.Errorf("client %s not found", clientID)
	}
	clientAddr := clientI.Addr

	f, err := os.Open(filepath)
	if err != nil {
		return err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return err
	}
	fileSize := stat.Size()
	totalChunks := int((fileSize + int64(chunkSize) - 1) / int64(chunkSize))

	// send metadata
	metadataStr := fmt.Sprintf("%s|%d|%d", filename, totalChunks, chunkSize)
	metaAck := make(chan struct{})
	s.packetGenerator(clientAddr, _metadata, []byte(metadataStr), 0, metaAck)

	// wait ack
	select {
	case <-metaAck:
		fmt.Println("Metadata ack received, starting file transfer")
	case <-time.After(20 * time.Second):
		return fmt.Errorf("timeout waiting metadata ack")
	}

	sem := make(chan struct{}, concurrentlyNum)
	var wg sync.WaitGroup
	buf := make([]byte, chunkSize)

	for chunkIndex := 0; chunkIndex < totalChunks; chunkIndex++ {
		n, err := io.ReadFull(f, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}
		chunkData := make([]byte, n)
		copy(chunkData, buf[:n])

		wg.Add(1)
		sem <- struct{}{}

		go func(idx int, data []byte) {
			defer wg.Done()
			defer func() { <-sem }()

			payload := make([]byte, 4+len(data))
			binary.BigEndian.PutUint32(payload[0:4], uint32(idx))
			copy(payload[4:], data)

			s.packetGenerator(clientAddr, _chunk, payload, 0, nil)

		}(chunkIndex, chunkData)
	}
	wg.Wait()
	return nil
}

func (s *Server) Start() {
	go s.MutexHandleActions()

	for i := 1; i <= 4; i++ {
		go s.udpWriteWorker(i)
	}

	for i := 1; i <= 4; i++ {
		go s.packetGeneratorWorker()
	}

	go s.udpReadWorker()

	for i := 1; i <= 4; i++ {
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
