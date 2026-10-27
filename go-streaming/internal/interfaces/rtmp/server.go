package rtmp

import (
	"fmt"
	"net"

	"quidb.dev/go-streaming/pkg/logger"
)

type Server struct {
	addr string
	log  *logger.Logger
}

func NewServer(addr string, log *logger.Logger) *Server {
	return &Server{addr: addr, log: log}
}

func (s *Server) Start() error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("không thể mở cổng RTMP %s: %w", s.addr, err)
	}
	defer listener.Close()

	s.log.Info("RTMP Ingest Server đang lắng nghe cổng %s (sẵn sàng đón OBS)", s.addr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			s.log.Error("Lỗi kết nối RTMP client: %v", err)
			continue
		}

		go s.handleClient(conn)
	}
}

func (s *Server) handleClient(conn net.Conn) {
	defer conn.Close()
	s.log.Info("Streamer vừa kết nối RTMP từ: %s", conn.RemoteAddr().String())

	// Đọc 1537 bytes Handshake C0/C1
	buf := make([]byte, 1537)
	n, err := conn.Read(buf)
	if err != nil {
		s.log.Error("Lỗi đọc handshake: %v", err)
		return
	}

	s.log.Info("Đã nhận %d bytes Handshake từ Streamer! (RTMP Protocol Version: %d)", n, buf[0])
}
