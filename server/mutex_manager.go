package server

import (
	"time"

	"udp/models"
)

func (s *Server) MutexHandleClientActions() {
	for mu := range s.muxClient {
		switch mu.Action {
		case "registration":
			client := &models.Client{ID: mu.Id, Addr: mu.Addr}
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
			s.pendingPackets[mu.PacketID] = models.PendingPacketsJob{
				Job:      models.Job{Addr: mu.Addr, Packet: mu.Packet},
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
			// copy := make(map[uint16]models.PendingPacketsJob)
			// for k, v := range s.pendingPackets {
			// 	copy[k] = v
			// }
			// mu.Reply <- copy
			snap := s.snapshot.Load()
			if snap == nil {
				mu.Reply <- make(map[uint16]models.PendingPacketsJob)
			} else {
				mu.Reply <- snap.(map[uint16]models.PendingPacketsJob)
			}
		}
	}
}

func (s *Server) updatePendingSnapshot() {
	// create new map and store it atomically
	cp := make(map[uint16]models.PendingPacketsJob, len(s.pendingPackets))
	for k, v := range s.pendingPackets {
		cp[k] = v
	}
	s.snapshot.Store(cp)
}