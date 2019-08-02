package multiplex

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	acceptBacklog = 1024
)

var ErrBrokenSession = errors.New("broken session")
var errRepeatSessionClosing = errors.New("trying to close a closed session")

type Session struct {
	id uint32

	// Used in Stream.Write. Add multiplexing headers, encrypt and add TLS header
	obfs Obfser
	// Remove TLS header, decrypt and unmarshall multiplexing headers
	deobfs Deobfser
	// This is supposed to read one TLS message, the same as GoQuiet's ReadTillDrain
	obfsedRead func(net.Conn, []byte) (int, error)

	SessionKey []byte

	// atomic
	nextStreamID uint32

	streamsM sync.Mutex
	streams  map[uint32]*Stream

	// Switchboard manages all connections to remote
	sb *switchboard

	addrs atomic.Value

	// For accepting new streams
	acceptCh chan *Stream

	closed uint32

	terminalMsg atomic.Value
}

func MakeSession(id uint32, valve *Valve, obfs Obfser, deobfs Deobfser, sessionKey []byte, obfsedRead func(net.Conn, []byte) (int, error)) *Session {
	sesh := &Session{
		id:           id,
		obfsedRead:   obfsedRead,
		nextStreamID: 1,
		obfs:         obfs,
		deobfs:       deobfs,
		SessionKey:   sessionKey,
		streams:      make(map[uint32]*Stream),
		acceptCh:     make(chan *Stream, acceptBacklog),
	}
	sesh.addrs.Store([]net.Addr{nil, nil})
	sesh.sb = makeSwitchboard(sesh, valve)
	go sesh.timeoutAfter(30 * time.Second)
	return sesh
}

func (sesh *Session) AddConnection(conn net.Conn) {
	sesh.sb.addConn(conn)
	addrs := []net.Addr{conn.LocalAddr(), conn.RemoteAddr()}
	sesh.addrs.Store(addrs)
}

func (sesh *Session) OpenStream() (*Stream, error) {
	if sesh.IsClosed() {
		return nil, ErrBrokenSession
	}
	id := atomic.AddUint32(&sesh.nextStreamID, 1) - 1
	// Because atomic.AddUint32 returns the value after incrementation
	stream := makeStream(id, sesh)
	sesh.streamsM.Lock()
	sesh.streams[id] = stream
	sesh.streamsM.Unlock()
	//log.Printf("Opening stream %v\n", id)
	return stream, nil
}

func (sesh *Session) Accept() (net.Conn, error) {
	if sesh.IsClosed() {
		return nil, ErrBrokenSession
	}
	stream := <-sesh.acceptCh
	if stream == nil {
		return nil, ErrBrokenSession
	}
	return stream, nil
}

func (sesh *Session) delStream(id uint32) {
	sesh.streamsM.Lock()
	delete(sesh.streams, id)
	if len(sesh.streams) == 0 {
		go sesh.timeoutAfter(30 * time.Second)
	}
	sesh.streamsM.Unlock()
}

// either fetch an existing stream or instantiate a new stream and put it in the dict, and return it
func (sesh *Session) getStream(id uint32, closingFrame bool) *Stream {
	// it would have been neater to use defer Unlock(), however it gives
	// non-negligable overhead and this function is performance critical
	sesh.streamsM.Lock()
	defer sesh.streamsM.Unlock()
	stream := sesh.streams[id]
	if stream != nil {
		return stream
	} else {
		if closingFrame {
			// If the stream has been closed and the current frame is a closing frame,
			// we return nil
			return nil
		} else {
			stream = makeStream(id, sesh)
			sesh.streams[id] = stream
			sesh.acceptCh <- stream
			return stream
		}
	}
}

func (sesh *Session) SetTerminalMsg(msg string) {
	sesh.terminalMsg.Store(msg)
}

func (sesh *Session) TerminalMsg() string {
	msg := sesh.terminalMsg.Load()
	if msg != nil {
		return msg.(string)
	} else {
		return ""
	}
}

func (sesh *Session) Close() error {
	atomic.StoreUint32(&sesh.closed, 1)
	sesh.streamsM.Lock()
	sesh.acceptCh <- nil
	for id, stream := range sesh.streams {
		// If we call stream.Close() here, streamsM will result in a deadlock
		// because stream.Close calls sesh.delStream, which locks the mutex.
		// so we need to implement a method of stream that closes the stream without calling
		// sesh.delStream
		go stream.closeNoDelMap()
		delete(sesh.streams, id)
	}
	sesh.streamsM.Unlock()

	sesh.sb.closeAll()
	return nil

}

func (sesh *Session) IsClosed() bool {
	return atomic.LoadUint32(&sesh.closed) == 1
}

func (sesh *Session) timeoutAfter(to time.Duration) {
	time.Sleep(to)
	sesh.streamsM.Lock()
	if len(sesh.streams) == 0 && !sesh.IsClosed() {
		sesh.streamsM.Unlock()
		sesh.SetTerminalMsg("timeout")
		sesh.Close()
	} else {
		sesh.streamsM.Unlock()
	}
}

func (sesh *Session) Addr() net.Addr { return sesh.addrs.Load().([]net.Addr)[0] }
