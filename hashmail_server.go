package subasta

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"sync"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightninglabs/lndclient"
	"github.com/lightninglabs/pool/auctioneerrpc"
	"github.com/lightninglabs/pool/sidecar"
	"github.com/lightningnetwork/lnd/tlv"
)

// streamID is the identifier of a stream.
type streamID [64]byte

// newStreamID creates a new stream given an ID as a byte slice.
func newStreamID(id []byte) streamID {
	var s streamID
	copy(s[:], id)

	return s
}

// readStream is the read side of the read pipe, which is implemented a
// buffered wrapper around the core reader.
type readStream struct {
	io.Reader

	// parentStream is a pointer to the parent stream. We keep this around
	// so we can return the stream after we're done using it.
	parentStream *stream

	// scratchBuf is a scratch buffer we'll use for decoding message from
	// the stream.
	scratchBuf [8]byte
}

// ReadNextMsg attempts to read the next message in the stream.
//
// NOTE: This will *block* until a new message is available.
func (r *readStream) ReadNextMsg() ([]byte, error) {
	// First, we'll decode the length of the next message from the stream
	// so we know how many bytes we need to read.
	msgLen, err := tlv.ReadVarInt(r, &r.scratchBuf)
	if err != nil {
		return nil, err
	}

	// Now that we know the length of the message, we'll make a limit
	// reader, then read all the encoded bytes until the EOF is emitted by
	// the reader.
	msgReader := io.LimitReader(r, int64(msgLen))
	return ioutil.ReadAll(msgReader)
}

// ReturnStream gives up the read stream by passing it back up through the
// payment stream.
func (r *readStream) ReturnStream() {
	r.parentStream.ReturnReadStream(r)
}

// writeStream is the write side of the read pipe. The stream itself is a
// buffered I/O wrapper around the write end of the io.Writer pipe.
type writeStream struct {
	io.Writer

	// parentStream is a pointer to the parent stream. We keep this around
	// so we can return the stream after we're done using it.
	parentStream *stream

	// scratchBuf is a scratch buffer we'll use for decoding message from
	// the stream.
	scratchBuf [8]byte
}

// WriteMsg attempts to write a message to the stream so it can be read using
// the read end of the stream.
//
// NOTE: If the buffer is full, then this call will block until the reader
// consumes bytes from the other end.
func (w *writeStream) WriteMsg(msg []byte) error {
	// TODO(roasbeef): max msg size?
	//  * limit # of outstanding msgs

	// As we're writing to a stream, we need to delimit each message with a
	// length prefix so the reader knows how many bytes to consume for each
	// message.
	//
	// TODO(roasbeef): actually needs to be single write?
	msgSize := uint64(len(msg))
	err := tlv.WriteVarInt(
		w, msgSize, &w.scratchBuf,
	)
	if err != nil {
		return err
	}

	// Next, we'll write the message directly to the stream.
	_, err = w.Write(msg)
	if err != nil {
		return err
	}

	return nil
}

// ReturnStream returns the write stream back to the parent stream.
func (w *writeStream) ReturnStream() {
	w.parentStream.ReturnWriteStream(w)
}

// stream is a unique pipe implemented using a subscription server, and expose
// over gRPC. Only a single writer and reader can exist within the stream at
// any given time.
type stream struct {
	sync.Mutex

	readStreamChan  chan *readStream
	writeStreamChan chan *writeStream
}

// newStream creates a new stream independent of any given stream ID.
func newStream() *stream {
	// Our stream is actually just a plain io.Pipe. This allows us to avoid
	// having to do things like rate limiting, etc as we can limit the
	// buffer size. In order to allow non-blocking writes (up to the buffer
	// size), but blocking reads, we'll utilize a series of two pipes.
	writeReadPipe, writeWritePipe := io.Pipe()
	readReadPipe, readWritePipe := io.Pipe()

	go func() {
		// Next, we'll launch a goroutine to copy over the bytes from
		// the pipe the writer will write to into the pipe the reader
		// will read from.
		_, err := io.Copy(
			readWritePipe,
			// This is where the buffering will happen, as the
			// writer writes to the write end of the pipe, this
			// goroutine will copy the bytes into the buffer until
			// its full, then attempt to write it to the write end
			// of the read pipe.
			bufio.NewReader(writeReadPipe),
		)
		_ = readWritePipe.CloseWithError(err)
		_ = writeReadPipe.CloseWithError(err)
	}()

	s := &stream{
		readStreamChan:  make(chan *readStream, 1),
		writeStreamChan: make(chan *writeStream, 1),
	}

	// We'll now initialize our stream by sending the read and write ends
	// to their respective holding channels.
	s.readStreamChan <- &readStream{
		Reader:       readReadPipe,
		parentStream: s,
	}
	s.writeStreamChan <- &writeStream{
		Writer:       writeWritePipe,
		parentStream: s,
	}

	return s
}

// ReturnReadStream returns the target read stream back to its holding channel.
func (s *stream) ReturnReadStream(r *readStream) {
	s.readStreamChan <- r
}

// ReturnWriteStream returns the target write stream back to its holding
// channel.
func (s *stream) ReturnWriteStream(w *writeStream) {
	s.writeStreamChan <- w
}

// RequestReadStream attempts to request the read stream from the main backing
// stream. If we're unable to obtain it before the timeout, then an error is
// returned.
func (s *stream) RequestReadStream() (*readStream, error) {
	log.Tracef("HashMailStream(%s): requesting read stream")

	select {
	case r := <-s.readStreamChan:
		return r, nil
	case <-time.After(leaseTimeout):
		return nil, fmt.Errorf("read stream occupied")
	}
}

// RequestWriteStream attempts to request the read stream from the main backing
// stream. If we're unable to obtain it before the timeout, then an error is
// returned.
func (s *stream) RequestWriteStream() (*writeStream, error) {
	log.Tracef("HashMailStream(%s): requesting write stream")

	select {
	case w := <-s.writeStreamChan:
		return w, nil
	case <-time.After(leaseTimeout):
		return nil, fmt.Errorf("read stream occupied")
	}
}

// hashMailServerConfig is the main config of the mail server.
type hashMailServerConfig struct {
	// IsAccountActive returns true of the passed public key belongs to an
	// active non-expired account) within the system.
	IsAccountActive func(context.Context, *btcec.PublicKey) bool

	// Signer is a reference to the current lnd signer client which will be
	// used to verify ECDSA signatures.
	Signer lndclient.SignerClient
}

// hashMailServer is an implementation of the HashMailServer gRPC service that
// implements a simple encrypted mailbox implemented as a series of read and
// write pipes.
type hashMailServer struct {
	sync.RWMutex
	streams map[streamID]*stream

	// TODO(roasbeef): index to keep track of total stream tallies

	quit chan struct{}

	cfg hashMailServerConfig
}

// newHashMailServer returns a new mail server instance given a valid config.
func newHashMailServer(cfg hashMailServerConfig) *hashMailServer {
	return &hashMailServer{
		streams: make(map[streamID]*stream),
		quit:    make(chan struct{}),
		cfg:     cfg,
	}
}

// ValidateStreamInit attempts to validate the authentication mechanism that is
// being used to claim a stream within the mail server.
func (h *hashMailServer) ValidateStreamInit(ctx context.Context,
	init *auctioneerrpc.CipherBoxInit) error {

	// At this time we only support one of two validation mechanisms: the
	// creator of the stream has an existing active Pool account, or we're
	// presented with a ticket hat was signed by an existing active Pool
	// account.
	switch {
	// In this case we need to validate that the account exists, and
	// the sig under the stream ID is valid.
	case init.GetAcctAuth() != nil:
		acctAuth := init.GetAcctAuth()

		pubKey, err := btcec.ParsePubKey(
			acctAuth.AcctKey, btcec.S256(),
		)
		if err != nil {
			return err
		}

		if !h.cfg.IsAccountActive(ctx, pubKey) {
			return fmt.Errorf("account %x isn't active",
				acctAuth.AcctKey)
		}

		var acctKey [33]byte
		copy(acctKey[:], acctAuth.AcctKey)

		validSig, err := h.cfg.Signer.VerifyMessage(
			ctx, init.Desc.StreamId, acctAuth.StreamSig, acctKey,
		)
		if err != nil {
			return fmt.Errorf("invalid stream sig: %v", err)
		}
		if !validSig {
			return fmt.Errorf("invalid sig: %v", err)
		}

	// In this case, the creator of the stream holds a valid sidecar
	// ticket, we'll allow them to pass if the ticket is legit, and
	// there exists an active account that signed the ticket.
	case init.GetSidecarAuth() != nil:
		sidecarAuth := init.GetSidecarAuth()

		ticket, err := sidecar.DecodeString(sidecarAuth.Ticket)
		if err != nil {
			return err
		}

		if !h.cfg.IsAccountActive(ctx, ticket.Offer.SignPubKey) {
			return fmt.Errorf("account isn't active")
		}

		err = sidecar.VerifyOffer(ctx, ticket, h.cfg.Signer)
		if err != nil {
			return err
		}

	default:

		return fmt.Errorf("invalid stream auth type")
	}

	// TODO(roasbeef): throttle the number of streams a given
	// ticket/account can have

	return nil
}

// InitStream attempts to initialize a new stream given a valid descriptor.
func (h *hashMailServer) InitStream(init *auctioneerrpc.CipherBoxInit,
) (*auctioneerrpc.CipherInitResp, error) {

	h.Lock()
	defer h.Unlock()

	streamID := newStreamID(init.Desc.StreamId)

	log.Debugf("Creating new HashMail Stream: %x", streamID)

	// The stream is already active, and we only allow a single session for
	// a given stream to exist.
	if _, ok := h.streams[streamID]; ok {
		return nil, fmt.Errorf("stream already active")
	}

	freshStream := newStream()

	h.streams[streamID] = freshStream

	return &auctioneerrpc.CipherInitResp{
		Resp: &auctioneerrpc.CipherInitResp_Success{},
	}, nil
}

// LookUpReadStream attempts to loop up a new stream. If the stream is found, then
// the stream is marked as being active. Otherwise, an error is returned.
func (h *hashMailServer) LookUpReadStream(streamID []byte) (*readStream, error) {

	h.RLock()
	defer h.RUnlock()

	stream, ok := h.streams[newStreamID(streamID)]
	if !ok {
		return nil, fmt.Errorf("stream not found")
	}

	return stream.RequestReadStream()
}

// LookUpWriteStream attempts to loop up a new stream. If the stream is found,
// then the stream is marked as being active. Otherwise, an error is returned.
func (h *hashMailServer) LookUpWriteStream(streamID []byte) (*writeStream, error) {

	h.RLock()
	defer h.RUnlock()

	stream, ok := h.streams[newStreamID(streamID)]
	if !ok {
		return nil, fmt.Errorf("stream not found")
	}

	return stream.RequestWriteStream()
}

// NewCipherBox attempts to create a new cipher box stream given a valid
// authentication mechanism. This call may fail if the stream is already
// active, or the authentication mechanism invalid.
func (h *hashMailServer) NewCipherBox(ctx context.Context,
	init *auctioneerrpc.CipherBoxInit) (*auctioneerrpc.CipherInitResp, error) {

	// Before we try to process the request, we'll do some basic user input
	// validation.
	switch {
	case init.Desc == nil:
		return nil, fmt.Errorf("cipher box descriptor required")

	case init.Desc.StreamId == nil:
		return nil, fmt.Errorf("stream_id required")

	case init.Auth == nil:
		return nil, fmt.Errorf("auth type required")
	}

	log.Debugf("New HashMail stream init: id=%x, auth=%v",
		init.Desc.StreamId, init.Auth)

	if err := h.ValidateStreamInit(ctx, init); err != nil {
		log.Debugf("Stream creation validation failed (id=%x): %v",
			init.Desc.StreamId, err)
		return nil, err
	}

	resp, err := h.InitStream(init)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// SendStream implements the client streaming call to utilize the write end of
// a stream to send a message to the read end.
func (h *hashMailServer) SendStream(readStream auctioneerrpc.HashMail_SendStreamServer) error {
	log.Debugf("New HashMail write stream pending...")

	// We'll need to receive the first message in order to determine if
	// this stream exists or not
	//
	// TODO(roasbeef): better way to control?
	cipherBox, err := readStream.Recv()
	if err != nil {
		return err
	}

	switch {
	case cipherBox.Desc == nil:
		return fmt.Errorf("cipher box descriptor required")

	case cipherBox.Desc.StreamId == nil:
		return fmt.Errorf("stream_id required")
	}

	log.Debugf("New HashMail write stream: id=%x",
		cipherBox.Desc.StreamId)

	// Now that we have the first message, we can attempt to look up the
	// given stream.
	writeStream, err := h.LookUpWriteStream(cipherBox.Desc.StreamId)
	if err != nil {
		return err
	}

	// Now that we know the stream is found, we'll make sure to mark the
	// write inactive if the client hangs up on their end.
	defer writeStream.ReturnStream()

	log.Tracef("Sending msg_len=%v to stream_id=%x", len(cipherBox.Msg),
		cipherBox.Desc.StreamId)

	// We'll send the first message into the stream, then enter our loop
	// below to continue to read from the stream and send it to the read
	// end.
	if err := writeStream.WriteMsg(cipherBox.Msg); err != nil {
		return err
	}

	for {
		// TODO(roasbeef): trigger a flush? can at least do one send
		// w/o blocking

		cipherBox, err := readStream.Recv()
		if err != nil {
			return err
		}

		log.Tracef("Sending msg_len=%v to stream_id=%x",
			len(cipherBox.Msg), cipherBox.Desc.StreamId)

		if err := writeStream.WriteMsg(cipherBox.Msg); err != nil {
			return err
		}
	}
}

// RecvStream implements the read end of the stream. A single client will have
// all messages written to the opposite side of the stream written to it for
// consumption.
func (h *hashMailServer) RecvStream(desc *auctioneerrpc.CipherBoxDesc,
	reader auctioneerrpc.HashMail_RecvStreamServer) error {

	// First, we'll attempt to locate the stream. We allow any single
	// entity that knows of the full stream ID to access the read end.
	readStream, err := h.LookUpReadStream(desc.StreamId)
	if err != nil {
		return err
	}

	log.Debugf("New HashMail read stream: id=%x", desc.StreamId)

	// If the reader hangs up, then we'll mark the stream as inactive so
	// another can take its place.
	defer readStream.ReturnStream()

	for {
		nextMsg, err := readStream.ReadNextMsg()
		if err != nil {
			return err
		}

		log.Tracef("Read %v bytes for HashMail stream_id=%x",
			len(nextMsg), desc.StreamId)

		err = reader.Send(&auctioneerrpc.CipherBox{
			Desc: desc,
			Msg:  nextMsg,
		})
		if err != nil {
			return err
		}
	}
}

var _ auctioneerrpc.HashMailServer = (*hashMailServer)(nil)
