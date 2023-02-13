// Copyright (c) 2023 RoseLoverX

package gogram

import (
	"context"
	"crypto/rsa"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amarnathcjd/gogram/internal/encoding/tl"
	"github.com/amarnathcjd/gogram/internal/mode"
	"github.com/amarnathcjd/gogram/internal/mtproto/messages"
	"github.com/amarnathcjd/gogram/internal/mtproto/objects"
	"github.com/amarnathcjd/gogram/internal/session"
	"github.com/amarnathcjd/gogram/internal/transport"
	"github.com/amarnathcjd/gogram/internal/utils"
	"github.com/pkg/errors"
)

const defaultTimeout = 65 * time.Second

type MTProto struct {
	Addr          string
	appID         int32
	socksProxy    *transport.Socks
	transport     transport.Transport
	stopRoutines  context.CancelFunc
	routineswg    sync.WaitGroup
	memorySession bool
	isConnected   bool

	authKey []byte

	authKeyHash []byte

	serverSalt int64
	encrypted  bool
	sessionId  int64

	mutex            sync.Mutex
	responseChannels *utils.SyncIntObjectChan
	expectedTypes    *utils.SyncIntReflectTypes

	seqNoMutex         sync.Mutex
	seqNo              int32
	lastMessageIDMutex sync.Mutex
	lastMessageID      int64

	sessionStorage session.SessionLoader

	PublicKey *rsa.PublicKey

	serviceChannel       chan tl.Object
	serviceModeActivated bool

	Logger *utils.Logger

	serverRequestHandlers []customHandlerFunc
}

type customHandlerFunc = func(i any) bool

type Config struct {
	AuthKeyFile    string
	StringSession  string
	SessionStorage session.SessionLoader
	MemorySession  bool
	AppID          int32

	ServerHost string
	PublicKey  *rsa.PublicKey
	DataCenter int
	LogLevel   string
	SocksProxy *transport.Socks
}

func NewMTProto(c Config) (*MTProto, error) {
	if c.SessionStorage == nil {
		if c.AuthKeyFile == "" {
			return nil, fmt.Errorf("auth key file is not specified")
		}

		c.SessionStorage = session.NewFromFile(c.AuthKeyFile)
	}

	s, err := c.SessionStorage.Load()
	if err != nil {
		if !(strings.Contains(err.Error(), session.ErrFileNotExists) || strings.Contains(err.Error(), session.ErrPathNotFound)) {
			return nil, fmt.Errorf("loading session: %w", err)
		}
	}
	var encrypted bool
	if s != nil {
		c.ServerHost = s.Hostname
		encrypted = true
	} else if c.StringSession != "" {
		encrypted = true
	}

	m := &MTProto{
		sessionStorage:        c.SessionStorage,
		Addr:                  c.ServerHost,
		encrypted:             encrypted,
		sessionId:             utils.GenerateSessionID(),
		serviceChannel:        make(chan tl.Object),
		PublicKey:             c.PublicKey,
		responseChannels:      utils.NewSyncIntObjectChan(),
		expectedTypes:         utils.NewSyncIntReflectTypes(),
		serverRequestHandlers: make([]customHandlerFunc, 0),
		Logger:                utils.NewLogger("GoGram [MTProto]").SetLevel(c.LogLevel),
		memorySession:         c.MemorySession,
		appID:                 c.AppID,
	}
	m.socksProxy = c.SocksProxy
	if c.StringSession != "" {
		_, err := m.ImportAuth(c.StringSession)
		if err != nil {
			return nil, fmt.Errorf("importing string session: %w", err)
		}
	} else {
		if s != nil {
			m.LoadSession(s)
		}
	}
	return m, nil
}

func (m *MTProto) ExportAuth() ([]byte, []byte, string, int, int32) {
	return m.authKey, m.authKeyHash, m.Addr, m.GetDC(), m.appID
}

func (m *MTProto) ImportRawAuth(authKey []byte, authKeyHash []byte, addr string, dc int, appID int32) (bool, error) {
	m.authKey, m.authKeyHash, m.Addr, m.appID = authKey, authKeyHash, addr, appID
	err := m.Reconnect(false)
	return err != nil, err
}

func (m *MTProto) ImportAuth(Session string) (bool, error) {
	StringSession := &session.StringSession{
		Encoded: Session,
	}
	AuthKey, AuthKeyHash, _, IpAddr, AppID, err := StringSession.Decode()
	if err != nil {
		return false, fmt.Errorf("decoding string session: %w", err)
	}
	m.authKey, m.authKeyHash, m.Addr, m.appID = AuthKey, AuthKeyHash, IpAddr, AppID
	if !m.memorySession {
		if err := m.SaveSession(); err != nil {
			return false, fmt.Errorf("saving session: %w", err)
		}
	}
	return true, nil
}

func (m *MTProto) GetDC() int {
	addr := m.Addr
	for k, v := range utils.DcList {
		if v == addr {
			return k
		}
	}
	return 4
}

func (m *MTProto) GetAppID() int32 {
	return m.appID
}

func (m *MTProto) ReconnectToNewDC(dc int) (*MTProto, error) {
	newAddr, isValid := utils.DcList[dc]
	if !isValid {
		return nil, fmt.Errorf("invalid DC: %d", dc)
	}
	m.sessionStorage.Delete()
	cfg := Config{
		DataCenter:    dc,
		PublicKey:     m.PublicKey,
		ServerHost:    newAddr,
		AuthKeyFile:   m.sessionStorage.Path(),
		MemorySession: false,
		LogLevel:      m.Logger.Lev(),
		SocksProxy:    m.socksProxy,
		AppID:         m.appID,
	}
	sender, _ := NewMTProto(cfg)
	sender.serverRequestHandlers = m.serverRequestHandlers
	m.stopRoutines()
	m.Logger.Info(fmt.Sprintf("User Migrated to -> [DC %d]", dc))
	err := sender.CreateConnection(true)
	if err != nil {
		return nil, fmt.Errorf("creating connection: %w", err)
	}
	return sender, nil
}

func (m *MTProto) ExportNewSender(dcID int, mem bool) (*MTProto, error) {
	newAddr := utils.DcList[dcID]
	execWorkDir, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("getting executable path: %w", err)
	}
	wd := filepath.Dir(execWorkDir)
	cfg := Config{
		DataCenter:    dcID,
		PublicKey:     m.PublicKey,
		ServerHost:    newAddr,
		AuthKeyFile:   filepath.Join(wd, "temp_sender.session"),
		MemorySession: mem,
		LogLevel:      m.Logger.Lev(),
		SocksProxy:    m.socksProxy,
		AppID:         m.appID,
	}
	if dcID == m.GetDC() {
		cfg.SessionStorage = m.sessionStorage
	}
	sender, _ := NewMTProto(cfg)
	m.Logger.Info("Exporting new sender for [DC " + strconv.Itoa(dcID) + "]")
	err = sender.CreateConnection(true)
	if err != nil {
		return nil, fmt.Errorf("creating connection: %w", err)
	}

	return sender, nil
}

func (m *MTProto) CreateConnection(withLog bool) error {
	ctx, cancelfunc := context.WithCancel(context.Background())
	m.stopRoutines = cancelfunc
	if withLog {
		m.Logger.Info("Connecting to " + m.Addr + " - [TCPFull]")
	}
	err := m.connect(ctx)
	if err != nil {
		return err
	}
	m.isConnected = true
	if withLog {
		m.Logger.Info("Connection to " + m.Addr + " - [TCPFull] established!")
	}
	m.startReadingResponses(ctx)
	if !m.encrypted {
		err = m.makeAuthKey()
		if err != nil {
			return err
		}
	}

	return nil
}

func (m *MTProto) connect(ctx context.Context) error {
	var err error
	m.transport, err = transport.NewTransport(
		m,
		transport.TCPConnConfig{
			Ctx:     ctx,
			Host:    m.Addr,
			Timeout: defaultTimeout,
			Socks:   m.socksProxy,
		},
		mode.Intermediate,
	)
	if err != nil {
		return fmt.Errorf("creating transport: %w", err)
	}

	closeOnCancel(ctx, m.transport)
	return nil
}

func (m *MTProto) makeRequest(data tl.Object, expectedTypes ...reflect.Type) (any, error) {
	resp, err := m.sendPacket(data, expectedTypes...)
	if err != nil {
		if strings.Contains(err.Error(), "use of closed network connection") || strings.Contains(err.Error(), "transport is closed") {
			m.Logger.Info("Connection Pipe Broken. Reconnecting to " + m.Addr + " - [TCPFull]")
			err = m.Reconnect(false)
			if err != nil {
				m.Logger.Error("Reconnecting error: " + err.Error())
				return nil, fmt.Errorf("reconnecting: %w", err)
			}
			return m.makeRequest(data, expectedTypes...)
		}
		return nil, fmt.Errorf("sending packet: %w", err)
	}
	response := <-resp
	switch r := response.(type) {
	case *objects.RpcError:
		realErr := RpcErrorToNative(r).(*ErrResponseCode)
		if strings.Contains(realErr.Message, "FLOOD_WAIT_") {
			m.Logger.Info("Flood wait detected on " + strings.ReplaceAll(reflect.TypeOf(data).Elem().Name(), "Params", "") + fmt.Sprintf(" request. Waiting for %d seconds", realErr.AdditionalInfo.(int)))
			time.Sleep(time.Duration(realErr.AdditionalInfo.(int)) * time.Second)
			return m.makeRequest(data, expectedTypes...)
		}
		return nil, realErr

	case *errorSessionConfigsChanged:
		return m.makeRequest(data, expectedTypes...)
	}

	return tl.UnwrapNativeTypes(response), nil
}

func (m *MTProto) InvokeRequestWithoutUpdate(data tl.Object, expectedTypes ...reflect.Type) error {
	_, err := m.sendPacket(data, expectedTypes...)
	if err != nil {
		return fmt.Errorf("sending packet: %w", err)
	}
	return err
}

func (m *MTProto) IsConnected() bool {
	return m.isConnected
}

func (m *MTProto) Disconnect() error {
	m.stopRoutines()
	m.isConnected = false
	// m.responseChannels.Close()
	return nil
}

func (m *MTProto) Terminate() error {
	m.stopRoutines()
	m.responseChannels.Close()
	m.Logger.Info("Disconnecting from " + m.Addr + " - [TcpFull]...")
	m.isConnected = false
	return nil
}

func (m *MTProto) Reconnect(WithLogs bool) error {
	err := m.Disconnect()
	if err != nil {
		return errors.Wrap(err, "disconnecting")
	}
	if WithLogs {
		m.Logger.Info("Reconnecting to " + m.Addr + " - [TcpFull]...")
	}

	err = m.CreateConnection(WithLogs)
	if err == nil && WithLogs {
		m.Logger.Info("Reconnected to " + m.Addr + " - [TcpFull]")
	}
	m.InvokeRequestWithoutUpdate(&utils.PingParams{
		PingID: 123456789,
	})
	return errors.Wrap(err, "recreating connection")
}

func (m *MTProto) Ping() time.Duration {
	start := time.Now()
	m.InvokeRequestWithoutUpdate(&utils.PingParams{
		PingID: 123456789,
	})
	return time.Since(start)
}

func (m *MTProto) startReadingResponses(ctx context.Context) {
	m.routineswg.Add(1)
	go func() {
		defer m.routineswg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				if !m.isConnected {
					m.Logger.Warn("Connection is not established with " + m.Addr + " - [TcpFull]")
					return
				}
				err := m.readMsg()
				switch err {
				case nil:
				case context.Canceled:
					return
				case io.EOF:
					err = m.Reconnect(false)
					if err != nil {
						m.Logger.Error(errors.Wrap(err, "reconnecting"))
					}
					return

				default:
					if e, ok := err.(transport.ErrCode); ok {
						if int(e) == 4294966892 {
							err = m.makeAuthKey()
							if err != nil {
								m.Logger.Error(errors.Wrap(err, "making auth key"))
							}
						} else {
							m.Logger.Error("Unhandled errorCode: " + fmt.Sprintf("%d", e))
						}
					}
					if strings.Contains(err.Error(), "required to reconnect!") {
						err = m.Reconnect(false)
						if err != nil {
							m.Logger.Error(errors.Wrap(err, "reconnecting error"))
						}
						return
					} else {
						err = m.Reconnect(false)
						if err != nil {
							m.Logger.Error(errors.Wrap(err, "reconnecting error"))
						}
					}
				}
			}
		}
	}()
}

func (m *MTProto) readMsg() error {
	if m.transport == nil {
		return errors.New("must setup connection before reading messages")
	}
	response, err := m.transport.ReadMsg()
	if err != nil {
		if e, ok := err.(transport.ErrCode); ok {
			return &ErrResponseCode{Code: int(e)}
		}
		switch err {
		case io.EOF, context.Canceled:
			return err
		default:
			return errors.Wrap(err, "reading message")
		}
	}

	if m.serviceModeActivated {
		var obj tl.Object
		obj, err = tl.DecodeUnknownObject(response.GetMsg())
		if err != nil {
			return errors.Wrap(err, "parsing object")
		}
		m.serviceChannel <- obj
		return nil
	}

	err = m.processResponse(response)
	if err != nil {
		return errors.Wrap(err, "Incomming Update")
	}
	return nil
}

func (m *MTProto) processResponse(msg messages.Common) error {
	var data tl.Object
	var err error
	if et, ok := m.expectedTypes.Get(msg.GetMsgID()); ok && len(et) > 0 {
		data, err = tl.DecodeUnknownObject(msg.GetMsg(), et...)
	} else {
		data, err = tl.DecodeUnknownObject(msg.GetMsg())
	}
	if err != nil {
		return fmt.Errorf("unmarshalling response: %w", err)
	}

messageTypeSwitching:
	switch message := data.(type) {
	case *objects.MessageContainer:
		for _, v := range *message {
			err := m.processResponse(v)
			if err != nil {
				return errors.Wrap(err, "processing item in container")
			}
		}

	case *objects.BadServerSalt:
		m.serverSalt = message.NewSalt
		if !m.memorySession {
			err := m.SaveSession()
			if err != nil {
				return errors.Wrap(err, "saving session")
			}
		}
		m.Reconnect(false)

		m.mutex.Lock()
		for _, k := range m.responseChannels.Keys() {
			v, _ := m.responseChannels.Get(k)
			v <- &errorSessionConfigsChanged{}
		}

		m.mutex.Unlock()

	case *objects.NewSessionCreated:
		m.serverSalt = message.ServerSalt
		if !m.memorySession {
			err := m.SaveSession()
			if err != nil {
				m.Logger.Error(errors.Wrap(err, "saving session"))
			}
		}

	case *objects.Pong, *objects.MsgsAck:

	case *objects.BadMsgNotification:
		return BadMsgErrorFromNative(message)
	case *objects.RpcResult:
		obj := message.Obj
		if v, ok := obj.(*objects.GzipPacked); ok {
			obj = v.Obj
		}
		err := m.writeRPCResponse(int(message.ReqMsgID), obj)
		if err != nil {
			return errors.Wrap(err, "writing RPC response")
		}

	case *objects.GzipPacked:
		// sometimes telegram server returns gzip for unknown reason. so, we are extracting data from gzip and
		// reprocess it again
		data = message.Obj
		goto messageTypeSwitching

	default:
		processed := false
		for _, f := range m.serverRequestHandlers {
			processed = f(message)
			if processed {
				break
			}
		}
		if !processed {
			m.Logger.Warn("Unhandled Incoming Update: " + fmt.Sprintf("%T", message))
		}
	}

	if (msg.GetSeqNo() & 1) != 0 {
		_, err := m.MakeRequest(&objects.MsgsAck{MsgIDs: []int64{int64(msg.GetMsgID())}})
		if err != nil {
			return errors.Wrap(err, "sending ack")
		}
	}

	return nil
}

func MessageRequireToAck(msg tl.Object) bool {
	switch msg.(type) {
	case *objects.MsgsAck:
		return false
	default:
		return true
	}
}

func closeOnCancel(ctx context.Context, c io.Closer) {
	go func() {
		<-ctx.Done()
		go func() {
			defer func() { recover() }()
			c.Close()
		}()
	}()
}
