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

	_chunkSize = 65000
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
	MsgType           byte
	Payload           []byte
	ClientAckPacketId uint16
	AckChan           chan struct{}
	RespChan          chan uint16
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

type PendingPacketsJob struct {
	Job
	LastSend time.Time
}

type Client struct {
	id             string
	serverAddr     *net.UDPAddr
	conn           *net.UDPConn
	writeQueue     chan Job
	parseQueue     chan Job
	genQueue       chan GenTask
	pendingPackets map[uint16]PendingPacketsJob

	muxPending chan Mutex
	snapshot   atomic.Value

	packetIDCounter uint32
	rttEstimate     atomic.Value

	// file receiving
	mux            sync.Mutex
	fileName       string
	totalChunks    int
	chunkSize      int
	receivedCount  int
	fileHandle     *os.File
	receivedChunks map[string]map[int]bool

	// file sending (when this client acts as sender)
	sendFilePath    string
	sendFileName    string
	sendFileHandle  *os.File
	sendTotalChunks int
	sendChunkSize   int
}

func NewClient(id string, server string) *Client {
	addr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		panic(err)
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		panic(err)
	}

	c := &Client{
		id:             id,
		serverAddr:     addr,
		conn:           conn,
		writeQueue:     make(chan Job, 5000),
		parseQueue:     make(chan Job, 5000),
		genQueue:       make(chan GenTask, 5000),
		pendingPackets: make(map[uint16]PendingPacketsJob),
		muxPending:     make(chan Mutex, 5000),
		receivedChunks: make(map[string]map[int]bool),
	}
	c.packetIDCounter = 0
	c.rttEstimate.Store(500 * time.Millisecond)

	c.snapshot.Store(make(map[uint16]PendingPacketsJob))

	return c
}

func (c *Client) writeWorker(id int) {
	for {
		job := <-c.writeQueue
		n, err := c.conn.Write(job.Packet)
		if n == 1209 {
			counter_write++
		}
		if err != nil {
			fmt.Printf("Writer %d error: %v\n", id, err)
		}
	}
}

func (c *Client) readWorker() {
	buffer := make([]byte, 65507)
	for {
		n, _, err := c.conn.ReadFromUDP(buffer)
		if n == 1209 {
			counter_read++
		}
		if err != nil {
			fmt.Println("Error receiving:", err)
			continue
		}
		packet := make([]byte, n)
		copy(packet, buffer[:n])
		c.parseQueue <- Job{Addr: c.serverAddr, Packet: packet}
	}
}

func (c *Client) packetParserWorker() {
	for {
		job := <-c.parseQueue
		c.PacketParser(job.Packet)
	}
}

func (c *Client) PacketParser(packet []byte) {
	if len(packet) < 5 {
		return
	}

	packetID := binary.BigEndian.Uint16(packet[0:2])
	msgType := packet[4]
	payload := packet[5:]

	switch msgType {
	case _message:
		c.packetGenerator(_ack, []byte("message received"), packetID, nil, nil)
		fmt.Println("Server:", string(payload))

	case _ack:
		fmt.Println("Server ack:", string(payload))
		c.muxPending <- Mutex{Action: "deletePending", PacketID: packetID}

	case _metadata:
		c.handleMetadata(payload, packetID)

	case _chunk:
		c.handleChunk(payload)

	case _requestChunk:
		c.handleRequestChunk(payload)

	case _done:
		c.handleDone()
	}
}

func (c *Client) packetGenerator(msgType byte, payload []byte, clientAckPacketId uint16, ackChan chan struct{}, resp chan uint16) {
	task := GenTask{
		MsgType:           msgType,
		Payload:           payload,
		ClientAckPacketId: clientAckPacketId,
		AckChan:           ackChan,
		RespChan:          resp,
	}
	c.genQueue <- task
}

func (c *Client) packetGeneratorWorker() {
	for task := range c.genQueue {
		packet := make([]byte, 2+2+1+len(task.Payload))

		pid := atomic.AddUint32(&c.packetIDCounter, 1)
		packetID := uint16(pid & 0xFFFF)

		binary.BigEndian.PutUint16(packet[2:4], 0)
		packet[4] = task.MsgType
		copy(packet[5:], task.Payload)

		if task.MsgType != _ack {
			binary.BigEndian.PutUint16(packet[0:2], packetID)
			// register as pending so ACK handling can delete and close ackChan
			// c.muxPending <- Mutex{Action: "addPending", PacketID: packetID, Addr: c.serverAddr, Packet: packet}
			// if task.AckChan != nil {
			// 	// start goroutine that closes ackChan when this packet is removed from pending snapshot
			// 	go func(pid uint16, ackCh chan struct{}) {
			// 		for {
			// 			snap := c.snapshot.Load().(map[uint16]PendingPacketsJob)
			// 			if _, ok := snap[pid]; !ok {
			// 				close(ackCh)
			// 				return
			// 			}
			// 			time.Sleep(100 * time.Millisecond)
			// 		}
			// 	}(packetID, task.AckChan)
			// }
			// if task.RespChan != nil {
			// 	task.RespChan <- packetID
			// }
		} else {
			binary.BigEndian.PutUint16(packet[0:2], task.ClientAckPacketId)
		}

		c.writeQueue <- Job{Addr: c.serverAddr, Packet: packet}
	}
}

func (c *Client) Register() {
	c.packetGenerator(_register, []byte(c.id), 0, nil, nil)
	fmt.Println("register:", c.id)
}

func (c *Client) Ping() {
	c.packetGenerator(_ping, []byte("ping"), 0, nil, nil)
	fmt.Println("ping")
}

func (c *Client) SendMessage(message string) {
	c.packetGenerator(_message, []byte(message), 0, nil, nil)
}

func (c *Client) handleMetadata(payload []byte, clientAckPacketId uint16) {
	// metadata-> filename|totalChunks|chunkSize
	meta := string(payload)
	parts := strings.Split(meta, "|")
	if len(parts) != 3 {
		fmt.Println("Invalid metadata format")
		return
	}

	c.mux.Lock()
	c.fileName = parts[0]
	c.totalChunks, _ = strconv.Atoi(parts[1])
	c.chunkSize, _ = strconv.Atoi(parts[2])
	c.receivedCount = 0
	c.receivedChunks[c.fileName] = make(map[int]bool)
	c.mux.Unlock()

	// open file for writing
	fpath := "fromServer_" + c.fileName
	f, err := os.OpenFile(fpath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Println("Error opening file for writing:", err)
		return
	}
	c.mux.Lock()
	c.fileHandle = f
	c.mux.Unlock()

	// ack metadata (so sender can stage/send)
	ackPayload := []byte("metadata received")
	c.packetGenerator(_ack, ackPayload, clientAckPacketId, nil, nil)
	fmt.Printf("Metadata received: %s (%d chunks, %d bytes each)\n", c.fileName, c.totalChunks, c.chunkSize)

	time.Sleep(10 * time.Millisecond)
	c.requestChunk(0)
}

func (c *Client) handleChunk(payload []byte) {
	if len(payload) < 4 {
		return
	}

	idx := int(binary.BigEndian.Uint32(payload[0:4]))
	data := payload[4:]

	c.mux.Lock()
	// duplication check
	if c.receivedChunks[c.fileName][idx] {
		c.mux.Unlock()
		fmt.Printf("Duplicate chunk %d ignored\n", idx)
		return
	}
	c.receivedChunks[c.fileName][idx] = true

	f := c.fileHandle
	filename := c.fileName
	chunkSize := c.chunkSize
	c.receivedCount++
	allDone := (c.receivedCount >= c.totalChunks)
	c.mux.Unlock()

	offset := int64(idx * chunkSize)
	_, err := f.WriteAt(data, offset)
	if err != nil {
		fmt.Println("Error writing chunk:", err)
		return
	}

	fmt.Printf("Chunk %d received and written (%d/%d)\n", idx, c.receivedCount, c.totalChunks)

	if allDone {
		c.packetGenerator(_done, []byte("done"), 0, nil, nil)

		c.mux.Lock()
		if c.fileHandle != nil {
			c.fileHandle.Close()
			c.fileHandle = nil
			delete(c.receivedChunks, c.fileName)
		}
		c.mux.Unlock()
		fmt.Printf("File saved from server: fromServer_%s\n", filename)
		return
	}

	// request next chunk
	c.requestChunk(idx + 1)
}

func (c *Client) requestChunk(idx int) {
	idxBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(idxBuf[0:4], uint32(idx))
	c.packetGenerator(_requestChunk, idxBuf, 0, nil, nil)
}

//

func (c *Client) SendFileToServer(path string) error {
	f, err := os.Open(path)
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
	filename := filepath.Base(path)

	c.mux.Lock()
	if c.sendFileHandle != nil {
		c.mux.Unlock()
		f.Close()
		return fmt.Errorf("another file is currently being sent")
	}
	c.sendFilePath = path
	c.sendFileName = filename
	c.sendFileHandle = f
	c.sendTotalChunks = totalChunks
	c.sendChunkSize = _chunkSize
	c.mux.Unlock()

	// send metadata
	metadataStr := fmt.Sprintf("%s|%d|%d", filename, totalChunks, _chunkSize)
	metaAck := make(chan struct{})
	c.packetGenerator(_metadata, []byte(metadataStr), 0, metaAck, nil)

	// wait ack from receiver
	select {
	case <-metaAck:
		fmt.Println("Metadata ack received, sender will wait for chunk requests")
	case <-time.After(20 * time.Second):
		// cleanup on timeout
		c.mux.Lock()
		if c.sendFileHandle != nil {
			c.sendFileHandle.Close()
			c.sendFileHandle = nil
		}
		c.mux.Unlock()
		return fmt.Errorf("timeout waiting metadata ack")
	}

	return nil
}

func (c *Client) handleRequestChunk(payload []byte) {
	if len(payload) < 4 {
		return
	}
	idx := int(binary.BigEndian.Uint32(payload[0:4]))

	c.mux.Lock()
	f := c.sendFileHandle
	total := c.sendTotalChunks
	chunkSz := c.sendChunkSize
	c.mux.Unlock()

	if f == nil {
		fmt.Println("No file currently staged for sending")
		return
	}

	if idx < 0 || idx >= total {
		fmt.Printf("Invalid chunk request %d (total %d)\n", idx, total)
		return
	}

	// read chunk at offset
	offset := int64(idx * chunkSz)
	buf := make([]byte, chunkSz)
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		fmt.Println("Error reading chunk:", err)
		return
	}
	chunkData := buf[:n]

	// prepare payload: 4 bytes index + data
	payloadSend := make([]byte, 4+len(chunkData))
	binary.BigEndian.PutUint32(payloadSend[0:4], uint32(idx))
	copy(payloadSend[4:], chunkData)

	// send chunk to server
	c.packetGenerator(_chunk, payloadSend, 0, nil, nil)
	fmt.Printf("Sent chunk %d (%d bytes)\n", idx, len(chunkData))
}

func (c *Client) handleDone() {
	fmt.Println("Done for transfer")

	// close any send file and cleanup
	c.mux.Lock()
	if c.sendFileHandle != nil {
		c.sendFileHandle.Close()
		c.sendFileHandle = nil
	}
	c.sendFilePath = ""
	c.sendFileName = ""
	c.sendTotalChunks = 0
	c.sendChunkSize = 0
	c.mux.Unlock()
}

func (c *Client) MutexHandleActions() {
	for mu := range c.muxPending {
		switch mu.Action {
		case "addPending":
			c.pendingPackets[mu.PacketID] = PendingPacketsJob{
				Job:      Job{Addr: mu.Addr, Packet: mu.Packet},
				LastSend: time.Now(),
			}
			c.updatePendingSnapshot()

		case "updatePending":
			if p, ok := c.pendingPackets[mu.PacketID]; ok {
				p.LastSend = time.Now()
				c.pendingPackets[mu.PacketID] = p
				c.updatePendingSnapshot()
			}

		case "deletePending":
			// if present, compute RTT from snapshot or c.pendingPackets
			if p, ok := c.pendingPackets[mu.PacketID]; ok {
				rtt := time.Since(p.LastSend)
				// EWMA update: new = old*(7/8) + rtt*(1/8)
				old := c.rttEstimate.Load().(time.Duration)
				newRTT := (old*7 + rtt) / 8
				c.rttEstimate.Store(newRTT)
			}
			delete(c.pendingPackets, mu.PacketID)
			c.updatePendingSnapshot()

		case "getAllPending":
			snap := c.snapshot.Load()
			if snap == nil {
				mu.Reply <- make(map[uint16]PendingPacketsJob)
			} else {
				mu.Reply <- snap.(map[uint16]PendingPacketsJob)
			}
		}
	}
}

func (c *Client) updatePendingSnapshot() {
	cp := make(map[uint16]PendingPacketsJob, len(c.pendingPackets))
	for k, v := range c.pendingPackets {
		cp[k] = v
	}
	c.snapshot.Store(cp)
}

func (c *Client) Start() {
	for i := 1; i <= 1; i++ {
		go c.writeWorker(i)
	}

	for i := 1; i <= 1; i++ {
		go c.packetGeneratorWorker()
	}

	go c.readWorker()

	for i := 1; i <= 1; i++ {
		go c.packetParserWorker()
	}

	go c.MutexHandleActions()
}

func main() {
	//173.208.144.109
	client := NewClient("2", "173.208.144.109:11000")
	client.Start()

	client.Register()

	ticker := time.NewTicker(28 * time.Second)
	go func() {
		for range ticker.C {
			client.Ping()
			fmt.Println("counter_write", counter_write)
			fmt.Println("counter_read", counter_read)
		}
	}()

	for {
		var input string
		fmt.Scan(&input)

		if strings.HasPrefix(input, "sendfile:") {
			path := strings.TrimPrefix(input, "sendfile:")
			fmt.Println("sending file:", path)
			if err := client.SendFileToServer(path); err != nil {
				fmt.Println("SendFile error:", err)
			}
			continue
		}

		client.SendMessage(input)
	}
}
