package server

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
	"udp/models"
)

func (s *Server) packetGenerator(addr *net.UDPAddr, msgType byte, payload []byte, clientAckPacketId uint16, ackChan chan struct{}) {
	task := models.GenTask{Addr: addr, MsgType: msgType, Payload: payload, ClientAckPacketId: clientAckPacketId, AckChan: ackChan}
	s.genQueue <- task
}

func (s *Server) pktGWorker() {
	for {
		task := <-s.genQueue
		packet := make([]byte, 2+2+1+len(task.Payload))
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		packetID := uint16(r.Intn(65535))

		binary.BigEndian.PutUint16(packet[2:4], 0)
		packet[4] = task.MsgType
		copy(packet[5:], task.Payload)

		if task.MsgType != models.Ack {
			binary.BigEndian.PutUint16(packet[0:2], packetID)
			s.muxPending <- models.Mutex{Action: "addPending", PacketID: packetID, Addr: task.Addr, Packet: packet}
			if task.AckChan != nil {
				s.muxClient <- models.Mutex{Action: "registerAckMetadata", PacketID: packetID, AckChan: task.AckChan}
			}
		} else {
			binary.BigEndian.PutUint16(packet[0:2], task.ClientAckPacketId)
		}

		s.builtpackets <- models.Job{Addr: task.Addr, Packet: packet}
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
	case models.Register:
		s.handleRegister(addr, payload, packetID)
	case models.Ping:
		s.handlePing(addr, packetID)
	case models.Message:
		s.handleMessage(addr, payload, packetID)
	case models.Ack:
		s.handleAck(packetID, payload)
	case models.Metadata:
		s.handleMetadata(addr, payload, packetID)
	case models.Chunk:
		s.handleChunk(addr, payload, packetID)
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

	// prepare file for this addr
	key := addr.String()
	fpath := "recv_" + filename

	f, err := os.OpenFile(fpath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Println("Error opening file for writing:", err)
		return
	}

	s.filesMu.Lock()
	if old, ok := s.files[key]; ok {
		old.Close()
	}
	s.files[key] = f
	s.meta[key] = models.FileMeta{
		Filename:    filename,
		TotalChunks: totalChunks,
		ChunkSize:   chunkSz,
		Received:    0,
	}
	s.filesMu.Unlock()

	// ack metadata back to client (client is expecting it)
	s.packetGenerator(addr, models.Ack, []byte("metadata received"), clientAckPacketId, nil)
	fmt.Printf("Metadata received from %s: %s (%d chunks, %d bytes each)\n", addr.String(), filename, totalChunks, chunkSz)
}

func (s *Server) handleChunk(addr *net.UDPAddr, payload []byte, clientAckPacketId uint16) {
	if len(payload) < 4 {
		return
	}
	idx := int(binary.BigEndian.Uint32(payload[0:4]))
	data := make([]byte, len(payload)-4)
	copy(data, payload[4:])

	key := addr.String()
	s.filesMu.Lock()
	f, okf := s.files[key]
	meta, okm := s.meta[key]
	if okm {
		meta.Received++
		s.meta[key] = meta
	}
	s.filesMu.Unlock()

	if !okf {
		fmt.Println("No file handle for", key)
		// still ack so sender knows it's received (or we could ignore)
		s.packetGenerator(addr, models.Ack, []byte(fmt.Sprintf("chunk %d received (no file)", idx)), clientAckPacketId, nil)
		return
	}

	// duplication
	if meta.ReceivedChunks[idx] {
        fmt.Printf("Chunk %d already received, skipping\n", idx)
        return
    }

	offset := int64(idx * meta.ChunkSize)
	_, err := f.WriteAt(data, offset)
	if err != nil {
		fmt.Println("Error writing chunk:", err)
		return
	}

	meta.ReceivedChunks[idx] = true
	// ack the chunk to client
	s.packetGenerator(addr, models.Ack, []byte(fmt.Sprintf("chunk %d received", idx)), clientAckPacketId, nil)
	fmt.Printf("Chunk %d received from %s (%d/%d)\n", idx, addr.String(), meta.Received, meta.TotalChunks)

	// if done, close
	if okm && meta.Received >= meta.TotalChunks {
		s.filesMu.Lock()
		if f2, ok := s.files[key]; ok {
			f2.Close()
			delete(s.files, key)
		}
		delete(s.meta, key)
		s.filesMu.Unlock()
		fmt.Printf("File saved from %s: recv_%s\n", addr.String(), meta.Filename)
	}
}

func (s *Server) fieldPacketTrackingWorker() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()

		reply := make(chan interface{})
		s.muxPending <- models.Mutex{Action: "getAllPending", Reply: reply}
		pendings := (<-reply).(map[uint16]models.PendingPacketsJob)

		for packetID, pending := range pendings {
			if now.Sub(pending.LastSend) >= 1*time.Second {
				// fmt.Printf("Retransmitting packet %d\n", packetID)
				s.builtpackets <- pending.Job
				s.muxPending <- models.Mutex{Action: "updatePending", PacketID: packetID}
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
}

func (s *Server) handleAck(packetID uint16, payload []byte) {
	fmt.Println("Client ack:", string(payload))
	s.muxPending <- models.Mutex{Action: "deletePending", PacketID: packetID}
}

func (s *Server) SendFileToClient(client *models.Client, filepath string, filename string) error {
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
	totalChunks := int((fileSize + int64(models.ChunkSize) - 1) / int64(models.ChunkSize))

	/// send metadata
	metadataStr := fmt.Sprintf("%s|%d|%d", filename, totalChunks, models.ChunkSize)
	metaAck := make(chan struct{})
	s.packetGenerator(client.Addr, models.Metadata, []byte(metadataStr), 0, metaAck)

	// wait ack
	select {
	case <-metaAck:
		fmt.Println("Metadata ack received, starting file transfer")
	case <-time.After(20 * time.Second):
		return fmt.Errorf("timeout waiting metadata ack")
	}

	buf := make([]byte, models.ChunkSize)

	for chunkIndex := 0; chunkIndex < totalChunks; chunkIndex++ {
		n, err := io.ReadFull(f, buf) // 65000   // f -> 1000
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}
		chunkData := make([]byte, n)
		copy(chunkData, buf[:n])

		payload := make([]byte, 4+len(chunkData))
		binary.BigEndian.PutUint32(payload[0:4], uint32(chunkIndex))
		copy(payload[4:], chunkData)

		s.packetGenerator(client.Addr, models.Chunk, payload, 0, nil)
		time.Sleep(30 * time.Millisecond)
	}
	return nil
}
