package main

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"os"
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
	fileSendLock   sync.Mutex
	fileSends      map[uint32]struct{} // basic marker that metadata ACK received
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
		writeQueue:     make(chan Job, 200),
		pendingPackets: make(map[uint16]pendingPacketsJob),
		parseQueue:     make(chan Job, 200),
		mux:            make(chan Mutex, 1000),
		fileSends:      make(map[uint32]struct{}),
	}
	return s, nil
}

func (s *Server) udpWriteWorker(id int) {
	for job := range s.writeQueue {
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
		now := time.Now()
		reply := make(chan interface{})
		s.mux <- Mutex{Action: "getAllPending", Reply: reply}
		pendings := (<-reply).(map[uint16]pendingPacketsJob)
		for packetID, pending := range pendings {
			if now.Sub(pending.LastSend) >= 10*time.Second {
				fmt.Printf("Retransmitting packet %d\n", packetID)
				s.writeQueue <- pending.Job
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

func (s *Server) packetGenerator(addr *net.UDPAddr, msgType byte, payload []byte, clientAckPacketId uint16) uint16 {
	packet := make([]byte, 2+2+1+len(payload))
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	packetID := uint16(r.Intn(65535))

	binary.BigEndian.PutUint16(packet[2:4], 0)

	packet[4] = msgType
	copy(packet[5:], payload)

	if msgType != _ack {
		binary.BigEndian.PutUint16(packet[0:2], packetID)
		s.mux <- Mutex{Action: "addPending", PacketID: packetID, Addr: addr, Packet: packet}
	} else {
		binary.BigEndian.PutUint16(packet[0:2], clientAckPacketId)
	}

	s.writeQueue <- Job{Addr: addr, Packet: packet}
	if msgType != _ack {
		return packetID
	}
	return 0
}

func (s *Server) packetParserWorker() {
	for job := range s.parseQueue {
		s.PacketParser(job.Addr, job.Packet)
	}
}

func (s *Server) PacketParser(addr *net.UDPAddr, packet []byte) {
	if len(packet) < 5 {
		return
	}
	packetID := binary.BigEndian.Uint16(packet[0:2])
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
		fmt.Println("[server: got metadata (unexpected) from", addr)
	case _chunk:
		fmt.Println("[server] got chunk (unexpected) from", addr)
	}
}

// ---- File sending: metadata then chunks ----
// payload for metadata:
// [fileID(4)][fileNameLen(2)][fileName][fileSize(8)][chunkSize(4)][totalChunks(4)]
// payload for chunk:
// [fileID(4)][chunkIndex(4)][totalChunks(4)][chunkData...]
func (s *Server) sendFile(addr *net.UDPAddr, filePath string, chunkSize int) error {
	// read file
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	fileName := filePath
	fileSize := int64(len(data))
	totalChunks := (len(data) + chunkSize - 1) / chunkSize
	// choose fileID
	fileID := rand.Uint32()

	// build metadata
	nameBytes := []byte(fileName)
	metaLen := 4 + 2 + len(nameBytes) + 8 + 4 + 4
	meta := make([]byte, metaLen)
	binary.BigEndian.PutUint32(meta[0:4], fileID)
	binary.BigEndian.PutUint16(meta[4:6], uint16(len(nameBytes)))
	copy(meta[6:6+len(nameBytes)], nameBytes)
	offset := 6 + len(nameBytes)
	binary.BigEndian.PutUint64(meta[offset:offset+8], uint64(fileSize))
	offset += 8
	binary.BigEndian.PutUint32(meta[offset:offset+4], uint32(chunkSize))
	offset += 4
	binary.BigEndian.PutUint32(meta[offset:offset+4], uint32(totalChunks))

	// send metadata and get packet id
	metaPktID := s.packetGenerator(addr, _metadata, meta, 0)
	fmt.Printf("sent metadata (fileID=%d) metaPktID=%d\n", fileID, metaPktID)

	// wait for metadata ACK by polling pending map: when the pending record for metaPktID is deleted, ack arrived
	for {
		time.Sleep(100 * time.Millisecond)
		reply := make(chan interface{})
		s.mux <- Mutex{Action: "getAllPending", Reply: reply}
		pendings := (<-reply).(map[uint16]pendingPacketsJob)
		if _, still := pendings[metaPktID]; !still {
			break // metadata ACK received
		}
	}

	// now send chunks (concurrently)
	var wg sync.WaitGroup
	for i := 0; i < totalChunks; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(data) {
			end = len(data)
		}
		chunkData := data[start:end]
		// build chunk payload
		chunkHdr := make([]byte, 4+4+4)
		binary.BigEndian.PutUint32(chunkHdr[0:4], fileID)
		binary.BigEndian.PutUint32(chunkHdr[4:8], uint32(i))
		binary.BigEndian.PutUint32(chunkHdr[8:12], uint32(totalChunks))
		payload := append(chunkHdr, chunkData...)

		wg.Add(1)
		go func(pl []byte) {
			defer wg.Done()
			s.packetGenerator(addr, _chunk, pl, 0)
		}(payload)
	}
	wg.Wait()
	fmt.Printf("finished sending fileID=%d\n", fileID)
	return nil
}

func (s *Server) handleRegister(addr *net.UDPAddr, payload []byte, clientAckPacketId uint16) {
	id := string(payload)
	s.mux <- Mutex{Action: "registration", Addr: addr, Id: id}
	s.packetGenerator(addr, _ack, []byte("Registered success"), clientAckPacketId)
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
	s.packetGenerator(addr, _ack, []byte("pong"), clientAckPacketId)
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
	s.packetGenerator(addr, _ack, []byte("message received"), clientAckPacketId)
	fmt.Printf("Message from %s: %s\n", client.ID, string(payload))
}

func (s *Server) handleAck(packetID uint16, payload []byte) {
	fmt.Println("Client:", string(payload))
	s.mux <- Mutex{Action: "deletePending", PacketID: packetID}
}

func (s *Server) MutexHandleActions() {
	for mu := range s.mux {
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
			pending := make(map[uint16]pendingPacketsJob)
			for k, v := range s.pendingPackets {
				pending[k] = v
			}
			mu.Reply <- pending
		}
	}
}

func (s *Server) MessageFromServerAnyTime() {
	for {
		var send, id, data string
		_, err := fmt.Scanln(&send, &id, &data)
		if err != nil {
			fmt.Println("input read error:", err)
			continue
		}

		reply := make(chan interface{})
		s.mux <- Mutex{Action: "clientByID", Id: id, Reply: reply}
		client := (<-reply).(*Client)
		if client == nil {
			fmt.Printf("Client %s not found\n", id)
			continue
		}

		if send == "send" {
			s.packetGenerator(client.Addr, _message, []byte(data), 0)
		} else if send == "sendfile" {
			chunkSize := 65000
			go func() {
				if err := s.sendFile(client.Addr, data, chunkSize); err != nil {
					fmt.Println("sendFile error:", err)
				}
			}()
		} else {
			fmt.Println("unknown command")
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
	fmt.Println("Server running on :9000")
	server.Start()
}
