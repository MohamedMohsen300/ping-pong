package server

import (
	"net"
	"os"
	"sync"
	"sync/atomic"

	"udp/models"
)

type Server struct {
	conn              *net.UDPConn
	clientsByID       map[string]*models.Client
	clientsByAddr     map[string]*models.Client
	writeQueue        chan models.Job
	pendingPackets    map[uint16]models.PendingPacketsJob
	parseQueue        chan models.Job
	genQueue          chan models.GenTask
	builtpackets      chan models.Job
	muxPending        chan models.Mutex
	muxClient         chan models.Mutex
	metaPendingMap    map[uint16]chan struct{}

	snapshot atomic.Value
	//
	filesMu sync.Mutex
	files   map[string]*os.File
	meta    map[string]models.FileMeta
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
		conn:              conn,
		clientsByID:       make(map[string]*models.Client),
		clientsByAddr:     make(map[string]*models.Client),
		writeQueue:        make(chan models.Job, 5000),
		pendingPackets:    make(map[uint16]models.PendingPacketsJob),
		parseQueue:        make(chan models.Job, 5000),
		genQueue:          make(chan models.GenTask, 5000),
		builtpackets:      make(chan models.Job, 5000),
		muxPending:        make(chan models.Mutex, 5000),
		muxClient:         make(chan models.Mutex, 5000),
		metaPendingMap:    make(map[uint16]chan struct{}),
		files:          make(map[string]*os.File),
		meta:           make(map[string]models.FileMeta),
	}
	s.snapshot.Store(make(map[uint16]models.PendingPacketsJob))
	return s, nil
}

func (s *Server) Start() {
	go s.MutexHandleActions()
	go s.MutexHandleClientActions()

	for i := 0; i < 4; i++ {
		go s.udpWriteWorker(i)
		go s.pktGWorker()
		go s.packetSender()
		go s.packetParserWorker()
	}

	go s.udpReadWorker()
	go s.fieldPacketTrackingWorker()
	go s.MessageFromServerAnyTime()

	select {}
}
