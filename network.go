// Copyright (c) 2025 @AmarnathCJD

package gogram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"reflect"

	"github.com/xAmarnath/gogram/internal/encoding/tl"
	"github.com/xAmarnath/gogram/internal/mtproto/messages"
	"github.com/xAmarnath/gogram/internal/mtproto/objects"
	"github.com/xAmarnath/gogram/internal/session"
	"github.com/xAmarnath/gogram/internal/transport"
	"github.com/xAmarnath/gogram/internal/utils"
)

// requestPacket preserves an uncertain RPC's identity for server deduplication.
type requestPacket struct {
	message   messages.Common
	seqNo     int32
	sessionID int64
	response  chan tl.Object
	changed   chan struct{}
	// Container rejections must not change the original RPC's identity.
	envelopeID       int64
	envelopeResponse chan tl.Object
}

func (m *MTProto) sendPacket(ctx context.Context, request tl.Object, msgID int64, packet *requestPacket, expectedTypes ...reflect.Type) (chan tl.Object, int64, error) {
	if msgID == 0 {
		if err := m.writeMu.Lock(ctx); err != nil {
			return nil, 0, err
		}
		defer m.writeMu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	msg, err := tl.Marshal(request)
	if err != nil {
		return nil, 0, fmt.Errorf("marshaling request: %w", err)
	}

	m.transportMu.Lock()
	tr := m.transport
	if tr == nil || !m.IsTcpActive() || m.disconnected.Load() || m.terminated.Load() {
		m.requestReconnect()
		m.transportMu.Unlock()
		return nil, 0, fmt.Errorf("transport is not active: %w", net.ErrClosed)
	}
	if packet != nil && packet.message != nil {
		if packet.sessionID != m.GetSessionID() {
			m.transportMu.Unlock()
			return nil, 0, ErrSessionChanged
		}
		msgID = packet.message.GetMsgID()
		if len(packet.response) != 0 {
			m.transportMu.Unlock()
			return packet.response, msgID, nil
		}
	}

	if msgID == 0 {
		msgID = m.genMsgID(m.timeOffset.Load())
	}

	var data messages.Common
	var seqNo int32
	if packet != nil && packet.message != nil {
		data = packet.message
		seqNo = packet.seqNo
		if original, ok := data.(*messages.Encrypted); ok {
			key, salt := m.keyForRequest(request)
			if !bytes.Equal(key, original.AuthKey) {
				m.transportMu.Unlock()
				return nil, msgID, ErrSessionChanged
			}
			body, err := tl.Marshal(&objects.MessageContainer{{MsgID: msgID, SeqNo: seqNo, Msg: original.Msg}})
			if err != nil {
				m.transportMu.Unlock()
				return nil, msgID, err
			}
			packet.envelopeID = m.genMsgID(m.timeOffset.Load())
			packet.envelopeResponse = make(chan tl.Object, 1)
			m.responseChannels.Add(packet.envelopeID, packet.envelopeResponse)
			data = &messages.Encrypted{Msg: body, MsgID: packet.envelopeID, AuthKey: key, AuthKeyHash: original.AuthKeyHash, Salt: salt, SessionID: packet.sessionID}
			seqNo = m.GetSeqNo()
		}
	} else if m.encrypted.Load() {
		key, salt := m.keyForRequest(request)
		data = &messages.Encrypted{Msg: msg, MsgID: msgID, AuthKey: key, AuthKeyHash: utils.AuthKeyHash(key), Salt: salt, SessionID: m.GetSessionID()}
	} else {
		data = &messages.Unencrypted{
			Msg:   msg,
			MsgID: msgID,
		}
	}

	if packet == nil || packet.message == nil {
		if isNotContentRelated(request) {
			seqNo = m.GetSeqNo()
		} else {
			seqNo = m.UpdateSeqNo()
		}
	}
	if packet != nil && packet.message == nil {
		*packet = requestPacket{message: data, seqNo: seqNo, sessionID: m.GetSessionID()}
	}
	if len(expectedTypes) > 0 && !isNullableResponse(request) {
		m.expectedTypes.Add(msgID, expectedTypes)
	}
	resp := m.getRespChannel()
	if isNullableResponse(request) {
		resp = make(chan tl.Object, 1)
		resp <- &objects.Null{}
	} else if !m.serviceModeActivated.Load() {
		if packet != nil {
			if packet.response == nil {
				packet.response = resp
				packet.changed = make(chan struct{}, 1)
				m.requestChanges.Store(msgID, packet.changed)
			} else {
				resp = packet.response
			}
			select {
			case <-packet.changed:
			default:
			}
		}
		m.responseChannels.Add(msgID, resp)
	}
	m.transportMu.Unlock()

	errorSendPacket := tr.WriteMsg(data, seqNo)
	if utils.IsTransportError(errorSendPacket) {
		m.reconnectTransport(tr)
	} else if errorSendPacket == nil && ctx.Err() != nil {
		errorSendPacket = ctx.Err()
	}

	if errorSendPacket != nil {
		if packet == nil {
			m.responseChannels.Delete(msgID)
			m.expectedTypes.Delete(msgID)
		}
		return nil, msgID, fmt.Errorf("writing message: %w", errorSendPacket)
	}
	return resp, msgID, nil
}

func (m *MTProto) reconnectTransport(tr transport.Transport) {
	m.transportMu.Lock()
	defer m.transportMu.Unlock()
	if m.transport != tr || m.terminated.Load() || m.disconnected.Load() {
		return
	}
	m.tcpState.SetActive(false)
	m.ctxCancelMutex.Lock()
	if m.ctxCancel != nil {
		m.ctxCancel()
	}
	m.ctxCancelMutex.Unlock()
	m.requestReconnect()
}

type transportReadError struct {
	transport transport.Transport
	cause     error
}

func (e *transportReadError) Error() string { return fmt.Sprintf("reading message: %v", e.cause) }
func (e *transportReadError) Unwrap() error { return e.cause }

func (m *MTProto) writeRPCResponse(msgID int64, data tl.Object) error {
	v, ok := m.responseChannels.Pop(msgID)
	if !ok {
		return errors.New("no response channel found for messageId " + fmt.Sprint(msgID))
	}

	m.expectedTypes.Delete(msgID)
	if err := safeSend(v, data); err != nil {
		return fmt.Errorf("sending response: %w", err)
	}

	return nil
}

func safeSend(ch chan tl.Object, obj tl.Object) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("channel closed: %v", r)
		}
	}()

	select {
	case ch <- obj:
		return nil // Successfully sent
	default:
		return fmt.Errorf("channel is full or closed")
	}
}

func (m *MTProto) getRespChannel() chan tl.Object {
	if m.serviceModeActivated.Load() {
		return m.serviceChannel
	}
	return make(chan tl.Object, 1)
}

func isNotContentRelated(t tl.Object) bool {
	switch t.(type) {
	case *objects.PingParams,
		*objects.PingDelayDisconnectParams,
		*utils.PingParams,
		*objects.MsgsAck,
		*objects.Pong,
		*objects.MsgsStateReq,
		*objects.MsgsStateInfo,
		*objects.MsgResendReq,
		*objects.HttpWaitParams:
		return true
	default:
		return false
	}
}

func isNullableResponse(t tl.Object) bool {
	switch t.(type) {
	case *objects.Pong, *objects.MsgsAck, *objects.MsgsStateInfo, *objects.HttpWaitParams:
		return true
	default:
		return false
	}
}

func (m *MTProto) GetSessionID() int64 {
	return m.sessionId.Load()
}

// GetSeqNo returns seqno
func (m *MTProto) GetSeqNo() int32 {
	return m.currentSeqNo.Load() * 2
}

func (m *MTProto) UpdateSeqNo() int32 {
	// https://core.telegram.org/mtproto/description#message-sequence-number-msg-seqno
	return (m.currentSeqNo.Add(1)-1)*2 + 1
}

// GetServerSalt returns current server salt
func (m *MTProto) GetServerSalt() int64 {
	m.authMu.RLock()
	defer m.authMu.RUnlock()
	if m.enablePFS && len(m.tempAuth.key) > 0 {
		return m.tempAuth.serverSalt
	}
	return m.serverSalt.Load()
}

// GetAuthKey returns the current auth key used for message encryption.
// In PFS mode, once a temp key is available, it's used for all traffic.
// The permanent key is retained on m.authKey for re-binding.
func (m *MTProto) GetAuthKey() []byte {
	m.authMu.RLock()
	defer m.authMu.RUnlock()
	if m.enablePFS && len(m.tempAuth.key) > 0 {
		return bytes.Clone(m.tempAuth.key)
	}
	return bytes.Clone(m.authKey)
}

func (m *MTProto) SetAuthKey(key []byte) {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	if !bytes.Equal(m.authKey, key) {
		m.tempAuth = tempAuthState{}
	}
	m.authKey = bytes.Clone(key)
	m.authKeyHash = utils.AuthKeyHash(m.authKey)
}

func (m *MTProto) AddCustomServerRequestHandler(handler func(i any) bool) {
	if handler == nil {
		return
	}
	m.handlersMu.Lock()
	defer m.handlersMu.Unlock()
	m.serverRequestHandlers = append(m.serverRequestHandlers, handler)
}

// AddRPCResponseHandler installs a hook that fires for every RPC response
// received from the server, before it is delivered to the caller waiting on
// the response channel. The hook is called synchronously on the read loop —
// keep it fast. Return values are ignored (the response is always delivered
// to the original caller regardless).
//
// This is used by the telegram package to tee update-carrying RPC responses
// (e.g. messages.sendMessage returning an Updates envelope) into the update
// pipeline so raw-update handlers observe them.
func (m *MTProto) AddRPCResponseHandler(handler func(i any)) {
	if handler == nil {
		return
	}
	m.handlersMu.Lock()
	defer m.handlersMu.Unlock()
	m.rpcResponseHandlers = append(m.rpcResponseHandlers, handler)
}

func (m *MTProto) SaveSession(mem bool) (err error) {
	key, hash := m.permanentAuth()
	sess := &session.Session{
		Key:      key,
		Hash:     hash,
		Salt:     m.serverSalt.Load(),
		Hostname: m.GetAddr(),
		AppID:    m.appID,
	}

	if !mem {
		m.Logger.Debug("saving session to '%s'", filepath.Base(m.sessionStorage.Path()))
		return m.sessionStorage.Store(sess)
	}

	return nil
}

func (m *MTProto) DeleteSession() (err error) {
	return m.sessionStorage.Delete()
}

func (m *MTProto) _loadSession(s *session.Session) {
	m.SetAuthKey(s.Key)
	m.serverSalt.Store(s.Salt)
	m.SetAddr(s.Hostname)
	m.dcID.Store(0)
	m.appID = s.AppID
}

func (m *MTProto) permanentAuth() ([]byte, []byte) {
	m.authMu.RLock()
	defer m.authMu.RUnlock()
	return bytes.Clone(m.authKey), bytes.Clone(m.authKeyHash)
}

func (m *MTProto) GetAuthKeyForHash(hash []byte) []byte {
	m.authMu.RLock()
	defer m.authMu.RUnlock()
	for _, key := range []struct{ key, hash []byte }{
		{m.authKey, m.authKeyHash}, {m.tempAuth.key, m.tempAuth.keyHash},
		{m.tempAuth.pendingKey, m.tempAuth.pendingKeyHash}, {m.tempAuth.previousKey, m.tempAuth.previousKeyHash},
	} {
		if len(hash) == 8 && bytes.Equal(hash, key.hash) {
			return bytes.Clone(key.key)
		}
	}
	return nil
}

func (m *MTProto) keyForRequest(request tl.Object) ([]byte, int64) {
	m.authMu.RLock()
	defer m.authMu.RUnlock()
	if _, bind := request.(*objects.AuthBindTempAuthKeyParams); bind {
		return bytes.Clone(m.tempAuth.pendingKey), m.tempAuth.pendingSalt
	}
	if m.enablePFS && len(m.tempAuth.key) > 0 {
		return bytes.Clone(m.tempAuth.key), m.tempAuth.serverSalt
	}
	return bytes.Clone(m.authKey), m.serverSalt.Load()
}

func (m *MTProto) updateSalt(msg messages.Common, salt int64) {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	if encrypted, ok := msg.(*messages.Encrypted); ok && len(encrypted.AuthKeyHash) > 0 {
		h := encrypted.AuthKeyHash
		switch {
		case bytes.Equal(h, m.tempAuth.pendingKeyHash):
			m.tempAuth.pendingSalt = salt
			return
		case bytes.Equal(h, m.tempAuth.keyHash):
			m.tempAuth.serverSalt = salt
			return
		case bytes.Equal(h, m.tempAuth.previousKeyHash):
			m.tempAuth.previousSalt = salt
			return
		}
	}
	m.serverSalt.Store(salt)
}
