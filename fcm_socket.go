package go_fcm_receiver

import (
	"context"
	"crypto/tls"
	"errors"
	"github.com/golang/protobuf/proto"
	"log"
	"strconv"
	"sync"
	"time"
)

type FCMSocketHandler struct {
	Socket                 *tls.Conn
	HeartbeatInterval      time.Duration
	state                  int
	data                   []byte
	dataMutex              sync.Mutex
	sizePacketSoFar        int
	messageTag             int
	messageSize            int
	handshakeComplete      bool
	isWaitingForData       bool
	heartbeatContextCancel context.CancelFunc
	onDataMutex            sync.Mutex
	OnMessage              func(messageTag int, messageObject interface{}) error
	OnClose                func()
}

func (f *FCMSocketHandler) StartSocketHandler() {
	go f.readData()
	go f.sendHeartbeatPings()
}

func (f *FCMSocketHandler) sendHeartbeatPings() {
	if f.HeartbeatInterval == 0 {
		f.HeartbeatInterval = time.Minute
	}
	var ctx context.Context
	ctx, f.heartbeatContextCancel = context.WithCancel(context.Background())
	for {
		select {
		case <-time.After(f.HeartbeatInterval):
			err := f.SendHeartbeatPing()
			if err != nil {
				return
			}
		case <-ctx.Done():
			return
		}

	}
}

func (f *FCMSocketHandler) SendHeartbeatPing() error {
	obj := &HeartbeatPing{}
	data, err := proto.Marshal(obj)
	if err != nil {
		log.Println(err)
		f.close()
		return err
	}
	_, err = f.Socket.Write(append([]byte{KHeartbeatPingTag, byte(proto.Size(obj))}, data...))
	if err != nil {
		log.Println(err)
		f.close()
		return err
	}
	return nil
}

func (f *FCMSocketHandler) readData() {
	for {
		var buffer []byte
		buffer = make([]byte, 1)
		_, err := f.Socket.Read(buffer)
		if err != nil {
			f.close()
			log.Println(err)
			return
		}
		f.dataMutex.Lock()
		f.data = append(f.data, buffer...)
		f.dataMutex.Unlock()
		go f.onData()
	}
}

func (f *FCMSocketHandler) onData() error {
	f.onDataMutex.Lock()
	defer f.onDataMutex.Unlock()

	if f.isWaitingForData {
		f.isWaitingForData = false
		err := f.waitForData()
		if err != nil {
			f.close()
			return err
		}
	}
	return nil
}

func (f *FCMSocketHandler) waitForData() error {
	minBytesNeeded := 0

	switch f.state {
	case MCS_VERSION_TAG_AND_SIZE:
		minBytesNeeded = KVersionPacketLen + KTagPacketLen + KSizePacketLenMin
		break
	case MCS_TAG_AND_SIZE:
		minBytesNeeded = KTagPacketLen + KSizePacketLenMin
		break
	case MCS_SIZE:
		minBytesNeeded = f.sizePacketSoFar + 1
		break
	case MCS_PROTO_BYTES:
		minBytesNeeded = f.messageSize
		break
	default:
		err := errors.New(`Unexpected state: ` + strconv.Itoa(f.state))
		log.Println(err)
		return err
	}
	f.dataMutex.Lock()
	if len(f.data) < minBytesNeeded {
		f.dataMutex.Unlock()
		f.isWaitingForData = true
		return nil
	}
	f.dataMutex.Unlock()

	switch f.state {
	case MCS_VERSION_TAG_AND_SIZE:
		err := f.onGotVersion()
		if err != nil {
			return err
		}
		break
	case MCS_TAG_AND_SIZE:
		err := f.onGotMessageTag()
		if err != nil {
			return err
		}
		break
	case MCS_SIZE:
		err := f.onGotMessageSize()
		if err != nil {
			return err
		}
		break
	case MCS_PROTO_BYTES:
		err := f.onGotMessageBytes()
		if err != nil {
			return err
		}
		break
	default:
		err := errors.New(`Unexpected state: ` + strconv.Itoa(f.state))
		log.Println(err)
		return err
	}

	return nil
}

func (f *FCMSocketHandler) onGotVersion() error {
	f.dataMutex.Lock()
	version := int(f.data[0])
	f.data = f.data[1:]
	f.dataMutex.Unlock()

	if version < KMCSVersion && version != 38 {
		err := errors.New("Got wrong version: " + strconv.Itoa(version))
		log.Println(err)
		return err
	}

	err := f.onGotMessageTag()
	if err != nil {
		return err
	}
	return nil
}

func (f *FCMSocketHandler) onGotMessageTag() error {
	f.dataMutex.Lock()
	f.messageTag = int(f.data[0])
	f.data = f.data[1:]
	f.dataMutex.Unlock()

	err := f.onGotMessageSize()
	if err != nil {
		return err
	}
	return nil
}

func (f *FCMSocketHandler) onGotMessageSize() error {
	incompleteSizePacket := false
	var pos int
	var err error
	f.dataMutex.Lock()
	f.messageSize, pos, err = ReadInt32(f.data)
	f.dataMutex.Unlock()
	pos += 1
	if err != nil {
		incompleteSizePacket = true
	}

	if incompleteSizePacket {
		f.sizePacketSoFar = pos
		f.state = MCS_SIZE
		err = f.waitForData()
		return err
	}

	f.dataMutex.Lock()
	f.data = f.data[pos:]
	f.dataMutex.Unlock()

	f.sizePacketSoFar = 0

	if f.messageSize > 0 {
		f.state = MCS_PROTO_BYTES
		err = f.waitForData()
		if err != nil {
			return err
		}
	} else {
		err = f.onGotMessageBytes()
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *FCMSocketHandler) onGotMessageBytes() error {
	f.dataMutex.Lock()
	if len(f.data) < f.messageSize {
		f.dataMutex.Unlock()
		f.state = MCS_PROTO_BYTES
		err := f.waitForData()
		if err != nil {
			return err
		}
		return nil
	}
	protobuf, err := f.buildProtobufFromTag(f.data[:f.messageSize])
	f.dataMutex.Unlock()
	if err != nil {
		return err
	}
	if protobuf == nil {
		f.data = f.data[f.messageSize:]
		err = errors.New("Unknown message tag " + strconv.Itoa(f.messageTag))
		log.Println(err)
		return err
	}

	if f.messageSize == 0 {
		err = f.OnMessage(f.messageTag, nil)
		if err != nil {
			return err
		}
		err = f.getNextMessage()
		if err != nil {
			return err
		}
		return nil
	}

	if len(f.data) < f.messageSize {
		f.state = MCS_PROTO_BYTES
		err = f.waitForData()
		if err != nil {
			return err
		}
		return nil
	}

	f.dataMutex.Lock()
	f.data = f.data[f.messageSize:]
	f.dataMutex.Unlock()

	err = f.OnMessage(f.messageTag, protobuf)
	if err != nil {
		return err
	}

	if f.messageTag == KLoginResponseTag {
		if !f.handshakeComplete {
			log.Println("Handshake complete")
			f.handshakeComplete = true
		}
	}

	err = f.getNextMessage()
	if err != nil {
		return err
	}
	return nil
}

func (f *FCMSocketHandler) getNextMessage() error {
	f.messageTag = 0
	f.messageSize = 0
	f.state = MCS_TAG_AND_SIZE
	err := f.waitForData()
	if err != nil {
		return err
	}
	return nil
}

func (f *FCMSocketHandler) buildProtobufFromTag(buffer []byte) (interface{}, error) {
	switch f.messageTag {
	case KHeartbeatPingTag:
		heartbeatPing, err := DecodeHeartbeatPing(buffer)
		if err != nil {
			return nil, err
		}
		return heartbeatPing, nil
	case KHeartbeatAckTag:
		heartbeatAck, err := DecodeHeartbeatAck(buffer)
		if err != nil {
			return nil, err
		}
		return heartbeatAck, nil
	case KLoginRequestTag:
		loginRequest, err := DecodeLoginRequest(buffer)
		if err != nil {
			return nil, err
		}
		return loginRequest, nil
	case KLoginResponseTag:
		loginResponse, err := DecodeLoginResponse(buffer)
		if err != nil {
			return nil, err
		}
		return loginResponse, nil
	case KCloseTag:
		closeObject, err := DecodeClose(buffer)
		if err != nil {
			return nil, err
		}
		return closeObject, nil
	case KIqStanzaTag:
		iqStanza, err := DecodeIqStanza(buffer)
		if err != nil {
			return nil, err
		}
		return iqStanza, nil
	case KDataMessageStanzaTag:
		dataMessageStanza, err := DecodeDataMessageStanza(buffer)
		if err != nil {
			return nil, err
		}
		return dataMessageStanza, nil
	case KStreamErrorStanzaTag:
		streamErrorStanza, err := DecodeStreamErrorStanza(buffer)
		if err != nil {
			return nil, err
		}
		return streamErrorStanza, nil
	default:
		return nil, nil
	}
}

func (f *FCMSocketHandler) Init() {
	f.state = MCS_VERSION_TAG_AND_SIZE
	f.dataMutex.Lock()
	f.data = []byte{}
	f.dataMutex.Unlock()
	f.sizePacketSoFar = 0
	f.messageTag = 0
	f.messageSize = 0
	f.handshakeComplete = false
	f.isWaitingForData = true
}

func (f *FCMSocketHandler) close() {
	f.heartbeatContextCancel()
	if f.Socket != nil {
		f.Socket.Close()
	}
	f.OnClose()
	f.Init()
}
