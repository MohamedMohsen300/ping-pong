// server.go
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	_register     = 1
	_ping         = 2
	_message      = 3
	_ack          = 4
	_metadata     = 5
	_chunk        = 6
	_requestChunk = 7
	_done         = 8

	_chunkSize = 20000
)

const (
	maxRetries = 3
	timeout    = 2 * time.Second
)

var counter_write = 0
var counter_read = 0

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

type PendingPacketsJob struct {
	Job
	LastSend time.Time
}

type Client struct {
	ID   string
	Addr *net.UDPAddr
}

type FileMeta struct {
	Filename    string
	TotalChunks int
	ChunkSize   int
	Received    int
}

type SendFileInfo struct {
	FilePath    string
	FileHandle  *os.File
	TotalChunks int
	ChunkSize   int
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
	pendingPackets map[uint16]PendingPacketsJob
	parseQueue     chan Job
	genQueue       chan GenTask
	builtpackets   chan Job
	muxPending     chan Mutex
	muxClient      chan Mutex
	metaPendingMap map[uint16]chan struct{}

	snapshot atomic.Value

	packetIDCounter uint32
	//
	filesMu sync.Mutex

	files map[string]*os.File
	meta  map[string]FileMeta

	sendFiles      map[string]SendFileInfo
	receivedChunks map[string]map[int]bool
}

func NewServer(addr string) (*Server, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}

	s := &Server{
		conn:           conn,
		clientsByID:    make(map[string]*Client),
		clientsByAddr:  make(map[string]*Client),
		writeQueue:     make(chan Job, 5000),
		pendingPackets: make(map[uint16]PendingPacketsJob),
		parseQueue:     make(chan Job, 5000),
		genQueue:       make(chan GenTask, 5000),
		builtpackets:   make(chan Job, 5000),
		muxPending:     make(chan Mutex, 5000),
		muxClient:      make(chan Mutex, 5000),
		metaPendingMap: make(map[uint16]chan struct{}),
		files:          make(map[string]*os.File),
		meta:           make(map[string]FileMeta),
		sendFiles:      make(map[string]SendFileInfo),
		receivedChunks: make(map[string]map[int]bool),
	}
	s.snapshot.Store(make(map[uint16]PendingPacketsJob))
	return s, nil
}

func (s *Server) udpWriteWorker(id int) {
	for {
		job := <-s.writeQueue
		n, err := s.conn.WriteToUDP(job.Packet, job.Addr)
		if n == 20009 {
			counter_write++
		}
		if err != nil {
			fmt.Printf("Writer %d error: %v\n", id, err)
		}
	}
}

func (s *Server) udpReadWorker() {
	buf := make([]byte, 65507)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if n == 20009 {
			counter_read++
		}
		if err != nil {
			fmt.Println("Read error:", err)
			continue
		}
		packet := make([]byte, n)
		copy(packet, buf[:n])
		s.parseQueue <- Job{Addr: addr, Packet: packet}
	}
}

func (s *Server) packetSender() {
	for {
		job := <-s.builtpackets
		s.writeQueue <- job
	}
}

func (s *Server) handleRegister(addr *net.UDPAddr, payload []byte, clientAckPacketId uint16) {
	id := string(payload)
	s.muxClient <- Mutex{Action: "registration", Addr: addr, Id: id}
	s.packetGenerator(addr, _ack, []byte("Registered success"), clientAckPacketId, nil)
	fmt.Println("Registered client:", id, addr)
}

func (s *Server) getClientByAddr(addr *net.UDPAddr) *Client {
	reply := make(chan interface{})
	s.muxClient <- Mutex{Action: "clientByAddr", Addr: addr, Reply: reply}
	client := (<-reply).(*Client)
	return client
}

func (s *Server) getClientById(id string) *Client {
	reply := make(chan interface{})
	s.muxClient <- Mutex{Action: "clientByID", Id: id, Reply: reply}
	client, _ := (<-reply).(*Client)
	return client
}

func (s *Server) handlePing(addr *net.UDPAddr, clientAckPacketId uint16) {
	client := s.getClientByAddr(addr)
	if client == nil {
		fmt.Println("Ping from unknown client:", addr)
		return
	}
	s.packetGenerator(addr, _ack, []byte("pong"), clientAckPacketId, nil)
	fmt.Printf("Ping from %s\n", client.ID)
	fmt.Println("counter_write", counter_write)
	fmt.Println("counter_read", counter_read)
}

func (s *Server) handleMessage(addr *net.UDPAddr, payload []byte, clientAckPacketId uint16) {
	client := s.getClientByAddr(addr)
	if client == nil {
		fmt.Println("Message from unknown client:", addr)
		return
	}
	s.packetGenerator(addr, _ack, []byte("message received"), clientAckPacketId, nil)
	fmt.Printf("Message from %s: %s\n", client.ID, string(payload))
}

func (s *Server) packetGenerator(addr *net.UDPAddr, msgType byte, payload []byte, clientAckPacketId uint16, ackChan chan struct{}) {
	task := GenTask{Addr: addr, MsgType: msgType, Payload: payload, ClientAckPacketId: clientAckPacketId, AckChan: ackChan}
	s.genQueue <- task
}

func (s *Server) pktGWorker() {
	for {
		task := <-s.genQueue
		packet := make([]byte, 2+2+1+len(task.Payload))
		pid := atomic.AddUint32(&s.packetIDCounter, 1)
		packetID := uint16(pid & 0xFFFF)

		binary.BigEndian.PutUint16(packet[2:4], 0)
		packet[4] = task.MsgType
		copy(packet[5:], task.Payload)

		if task.MsgType != _ack {
			binary.BigEndian.PutUint16(packet[0:2], packetID)
			// keep pending logic minimal for metadata ACKs
			if task.AckChan != nil {
				s.muxClient <- Mutex{Action: "registerAckMetadata", PacketID: packetID, AckChan: task.AckChan}
			}
		} else {
			binary.BigEndian.PutUint16(packet[0:2], task.ClientAckPacketId)
		}

		s.builtpackets <- Job{Addr: task.Addr, Packet: packet}
	}
}

func (s *Server) packetParserWorker() {
	for {
		job := <-s.parseQueue
		s.PacketParser(job.Addr, job.Packet)
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
		s.handleMetadata(addr, payload, packetID)
	case _chunk:
		s.handleChunk(addr, payload)
	case _requestChunk:
		s.handleRequestChunk(addr, payload)
	case _done:
		s.handleDone(addr)
	}
}

func (s *Server) handleMetadata(addr *net.UDPAddr, payload []byte, clientAckPacketId uint16) {
	// payload: filename|totalChunks|chunkSize
	parts := strings.Split(string(payload), "|")
	if len(parts) != 3 {
		fmt.Println("Invalid metadata from", addr.String())
		return
	}
	filename := parts[0]
	totalChunks, _ := strconv.Atoi(parts[1])
	chunkSz, _ := strconv.Atoi(parts[2])

	key := addr.String()
	fpath := "fromClient_" + filename

	f, err := os.OpenFile(fpath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Println("Error opening file for writing:", err)
		return
	}

	s.filesMu.Lock()
	// store file handle and metadata for receiving
	s.files[key] = f
	s.meta[key] = FileMeta{
		Filename:    filename,
		TotalChunks: totalChunks,
		ChunkSize:   chunkSz,
		Received:    0,
	}
	// init receivedChunks map
	s.receivedChunks[key] = make(map[int]bool)
	s.filesMu.Unlock()

	// ack metadata
	s.packetGenerator(addr, _ack, []byte("metadata received"), clientAckPacketId, nil)
	fmt.Printf("Metadata received from %s: %s (%d chunks, %d bytes each)\n", addr.String(), filename, totalChunks, chunkSz)

	time.Sleep(10 * time.Millisecond)
	s.requestChunk(addr, 0)
}

func (s *Server) handleChunk(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 4 {
		return
	}
	idx := int(binary.BigEndian.Uint32(payload[0:4]))
	data := payload[4:]

	key := addr.String()

	s.filesMu.Lock()
	// duplicate check
	got := false
	if _, ok := s.receivedChunks[key]; ok {
		if s.receivedChunks[key][idx] {
			got = true
		}
	}
	if !got {
		// mark received and increment meta counter
		if _, ok := s.receivedChunks[key]; !ok {
			s.receivedChunks[key] = make(map[int]bool)
		}
		s.receivedChunks[key][idx] = true
		if meta, ok := s.meta[key]; ok {
			meta.Received++
			s.meta[key] = meta
		}
	}
	f, okf := s.files[key]
	meta, okm := s.meta[key]
	s.filesMu.Unlock()

	if !okf {
		fmt.Println("No file handle for", key)
		return
	}

	// write directly to disk at offset
	offset := int64(idx * meta.ChunkSize)
	_, err := f.WriteAt(data, offset)
	if err != nil {
		fmt.Println("Error writing chunk:", err)
		return
	}

	fmt.Printf("Chunk %d received from %s (%d/%d)\n", idx, addr.String(), s.meta[key].Received, s.meta[key].TotalChunks)

	// if done, close and cleanup; else request next chunk
	if okm && s.meta[key].Received >= s.meta[key].TotalChunks {
		// close and cleanup
		s.filesMu.Lock()
		f.Close()
		delete(s.files, key)
		delete(s.meta, key)
		delete(s.receivedChunks, key)
		s.filesMu.Unlock()
		// send done to sender
		s.packetGenerator(addr, _done, []byte("done"), 0, nil)
		fmt.Printf("File saved from %s: fromClient_%s\n", addr.String(), meta.Filename)
	} else {
		// request next chunk index (with retry)
		nextIdx := idx + 1
		s.requestChunk(addr, nextIdx)
	}
}

func (s *Server) requestChunk(addr *net.UDPAddr, idx int) {
	idxBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(idxBuf[0:4], uint32(idx))
	s.packetGenerator(addr, _requestChunk, idxBuf, 0, nil)
}

func (s *Server) handleRequestChunk(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 4 {
		return
	}
	idx := int(binary.BigEndian.Uint32(payload[0:4]))
	key := addr.String()

	s.filesMu.Lock()
	sendInfo, ok := s.sendFiles[key]
	s.filesMu.Unlock()

	if !ok {
		fmt.Printf("No sending file staged for %s (requested chunk %d)\n", key, idx)
		return
	}

	if idx < 0 || idx >= sendInfo.TotalChunks {
		fmt.Printf("Invalid chunk request %d (total %d) from %s\n", idx, sendInfo.TotalChunks, key)
		return
	}

	offset := int64(idx * sendInfo.ChunkSize)
	buf := make([]byte, sendInfo.ChunkSize)
	n, err := sendInfo.FileHandle.ReadAt(buf, offset)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		fmt.Println("Error reading file for sending chunk:", err)
		return
	}
	chunkData := buf[:n]

	// prepare payload: 4 bytes index + data
	payloadSend := make([]byte, 4+len(chunkData))
	binary.BigEndian.PutUint32(payloadSend[0:4], uint32(idx))
	copy(payloadSend[4:], chunkData)

	// send chunk
	s.packetGenerator(addr, _chunk, payloadSend, 0, nil)
	fmt.Printf("Sent chunk %d to %s (%d bytes)\n", idx, key, len(chunkData))
}

func (s *Server) handleDone(addr *net.UDPAddr) {
	key := addr.String()
	s.filesMu.Lock()
	if info, ok := s.sendFiles[key]; ok {
		if info.FileHandle != nil {
			info.FileHandle.Close()
		}
		delete(s.sendFiles, key)
		fmt.Printf("Sent (file %s) for %s \n", info.FilePath, key)
	}
	s.filesMu.Unlock()
}

func (s *Server) handleAck(packetID uint16, payload []byte) {
	fmt.Println("Client ack:", string(payload))
	s.muxPending <- Mutex{Action: "deletePending", PacketID: packetID}
}

func (s *Server) SendFileToClient(client *Client, filepathStr string, filename string) error {
	f, err := os.Open(filepathStr)
	if err != nil {
		return err
	}

	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}

	fileSize := stat.Size()
	totalChunks := int((fileSize + int64(_chunkSize) - 1) / int64(_chunkSize))

	/// send metadata
	metadataStr := fmt.Sprintf("%s|%d|%d", filename, totalChunks, _chunkSize)
	metaAck := make(chan struct{})
	s.packetGenerator(client.Addr, _metadata, []byte(metadataStr), 0, metaAck)

	// wait ack
	select {
	case <-metaAck:
		s.filesMu.Lock()
		s.sendFiles[client.Addr.String()] = SendFileInfo{
			FilePath:    filepathStr,
			FileHandle:  f,
			TotalChunks: totalChunks,
			ChunkSize:   _chunkSize,
		}
		s.filesMu.Unlock()
		fmt.Println("Metadata ack received, sender staged and waiting for chunk requests")
	case <-time.After(20 * time.Second):
		f.Close()
		return fmt.Errorf("timeout waiting metadata ack")
	}

	return nil
}

func (s *Server) MutexHandleClientActions() {
	for mu := range s.muxClient {
		switch mu.Action {
		case "registration":
			client := &Client{ID: mu.Id, Addr: mu.Addr}
			s.clientsByID[mu.Id] = client
			s.clientsByAddr[mu.Addr.String()] = client

		case "clientByAddr":
			mu.Reply <- s.clientsByAddr[mu.Addr.String()]

		case "clientByID":
			mu.Reply <- s.clientsByID[mu.Id]

		case "registerAckMetadata":
			if mu.AckChan != nil {
				s.metaPendingMap[mu.PacketID] = mu.AckChan
			}
		}
	}
}

func (s *Server) MutexHandleActions() {
	for mu := range s.muxPending {
		switch mu.Action {
		case "addPending":
			s.pendingPackets[mu.PacketID] = PendingPacketsJob{
				Job:      Job{Addr: mu.Addr, Packet: mu.Packet},
				LastSend: time.Now(),
			}
			s.updatePendingSnapshot()

		case "updatePending":
			if p, ok := s.pendingPackets[mu.PacketID]; ok {
				p.LastSend = time.Now()
				s.pendingPackets[mu.PacketID] = p
				s.updatePendingSnapshot()
			}

		case "deletePending":
			delete(s.pendingPackets, mu.PacketID)
			if ch, ok := s.metaPendingMap[mu.PacketID]; ok {
				close(ch)
				delete(s.metaPendingMap, mu.PacketID)
			}
			s.updatePendingSnapshot()

		case "getAllPending":
			snap := s.snapshot.Load()
			if snap == nil {
				mu.Reply <- make(map[uint16]PendingPacketsJob)
			} else {
				mu.Reply <- snap.(map[uint16]PendingPacketsJob)
			}
		}
	}
}

func (s *Server) updatePendingSnapshot() {
	cp := make(map[uint16]PendingPacketsJob, len(s.pendingPackets))
	for k, v := range s.pendingPackets {
		cp[k] = v
	}
	s.snapshot.Store(cp)
}

func (s *Server) MessageFromServerAnyTime() {
	for {
		var send, id, msg string
		_, err := fmt.Scanln(&send, &id, &msg)
		if err != nil {
			fmt.Println("Error reading input:", err)
			continue
		}

		client := s.getClientById(id)
		if client == nil {
			fmt.Printf("Client %s not found\n", id)
			continue
		}

		if send == "send" {
			s.packetGenerator(client.Addr, _message, []byte(msg), 0, nil)
		} else if send == "sendfile" {
			err := s.SendFileToClient(client, msg, filepath.Base(msg))
			if err != nil {
				fmt.Println("SendFile error:", err)
			}
		}
	}
}

func (s *Server) Start() {
	go s.MutexHandleActions()
	go s.MutexHandleClientActions()

	for i := 0; i < 1; i++ {
		go s.udpWriteWorker(i)
		go s.pktGWorker()
		go s.packetSender()
		go s.packetParserWorker()
	}

	go s.udpReadWorker()
	go s.MessageFromServerAnyTime()

	select {}
}

func main() {
	s, err := NewServer(":11000")
	if err != nil {
		panic(err)
	}

	fmt.Println("Server running on port 11000...... :)")
	s.Start()
}
