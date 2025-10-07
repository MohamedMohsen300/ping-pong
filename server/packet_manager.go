package server

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
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
			s.mux <- models.Mutex{Action: "addPending", PacketID: packetID, Addr: task.Addr, Packet: packet}
			if task.AckChan != nil {
				s.mux <- models.Mutex{Action: "registerAckMetadata", PacketID: packetID, AckChan: task.AckChan}
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
		fmt.Printf("Received metadata from %s: %s\n", addr.String(), string(payload))
	}
}

func (s *Server) fieldPacketTrackingWorker() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()

		reply := make(chan interface{})
		s.mux <- models.Mutex{Action: "getAllPending", Reply: reply}
		pendings := (<-reply).(map[uint16]models.PendingPacketsJob)

		for packetID, pending := range pendings {
			if now.Sub(pending.LastSend) >= 1*time.Second {
				// fmt.Printf("Retransmitting packet %d\n", packetID)
				s.builtpackets <- pending.Job
				s.mux <- models.Mutex{Action: "updatePending", PacketID: packetID}
				// time.Sleep(10 * time.Millisecond)
			}
		}
	}
}

func (s *Server) handleAck(packetID uint16, payload []byte) {
	fmt.Println("Client ack:", string(payload))
	s.mux <- models.Mutex{Action: "deletePending", PacketID: packetID}
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
		// time.Sleep(40 * time.Millisecond) // no 30 ms
	}
	return nil
}
