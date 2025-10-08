package server

import (
	"net"

	"udp/models"
)

type Server struct {
	conn           *net.UDPConn
	clientsByID    map[string]*models.Client
	clientsByAddr  map[string]*models.Client
	writeQueue     chan models.Job
	pendingPackets map[uint16]models.PendingPacketsJob
	parseQueue     chan models.Job
	genQueue       chan models.GenTask
	builtpackets   chan models.Job
	mux            chan models.Mutex
	metaPendingMap map[uint16]chan struct{}
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
		clientsByID:    make(map[string]*models.Client),
		clientsByAddr:  make(map[string]*models.Client),
		writeQueue:     make(chan models.Job, 1000),
		pendingPackets: make(map[uint16]models.PendingPacketsJob),
		parseQueue:     make(chan models.Job, 1000),
		genQueue:       make(chan models.GenTask, 1000),
		builtpackets:   make(chan models.Job, 1000),
		mux:            make(chan models.Mutex, 1000),
		metaPendingMap: make(map[uint16]chan struct{}),
	}

	return s, nil
}

func (s *Server) Start() {
	go s.MutexHandleActions()

	for i := 0; i < 4; i++ {
		go s.udpWriteWorker(i)
		go s.pktGWorker()
		go s.packetSender()
		go s.packetParserWorker()
	}

	go s.udpReadWorker()
	// go s.fieldPacketTrackingWorker()
	go s.MessageFromServerAnyTime()

	select {}
}
