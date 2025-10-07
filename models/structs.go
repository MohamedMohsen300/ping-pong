package models

import (
	"net"
	"time"
)

const (
	Register = 1
	Ping     = 2
	Message  = 3
	Ack      = 4
	Metadata = 5
	Chunk    = 6
	//total - (pktID + encDec + msgtype + chunkIndex)
	ChunkSize =  30000 //65507 - (2 + 2 + 1 + 4) // 65507 - 9 = 65498
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

type PendingPacketsJob struct {
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
