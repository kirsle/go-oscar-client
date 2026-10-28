package client

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log/slog"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mk6i/open-oscar-server/wire"
)

// fakeService accepts one connection on a local listener, completes the
// service signon handshake and hands the server end to serve.
func fakeService(t *testing.T, serve func(flapc *wire.FlapClient)) string {
	t.Helper()
	return fakeServiceTLS(t, nil, serve)
}

// fakeServiceTLS is fakeService with a listener that requires TLS, unless
// tlsCfg is nil.
func fakeServiceTLS(t *testing.T, tlsCfg *tls.Config, serve func(flapc *wire.FlapClient)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = nc.Close() }()
		flapc := wire.NewFlapClient(100, nc, nc)
		if err := flapc.SendSignonFrame(nil); err != nil {
			return
		}
		if _, err := flapc.ReceiveSignonFrame(); err != nil {
			return
		}
		if err := flapc.SendSNAC(wire.SNACFrame{FoodGroup: wire.OService, SubGroup: wire.OServiceHostOnline},
			wire.SNAC_0x01_0x03_OServiceHostOnline{FoodGroups: []uint16{wire.OService}}); err != nil {
			return
		}
		serve(flapc)
	}()
	return ln.Addr().String()
}

// selfSignedCert returns a certificate for host and a pool that trusts it.
func selfSignedCert(t *testing.T, host string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

func receiveFrame(flapc *wire.FlapClient) (wire.SNACFrame, error) {
	flap, err := flapc.ReceiveFLAP()
	if err != nil {
		return wire.SNACFrame{}, err
	}
	frame := wire.SNACFrame{}
	err = wire.UnmarshalBE(&frame, bytes.NewReader(flap.Payload))
	return frame, err
}

func TestConn_request(t *testing.T) {
	addr := fakeService(t, func(flapc *wire.FlapClient) {
		// user info query: an unrelated server SNAC arrives before the reply
		frame, err := receiveFrame(flapc)
		if err != nil {
			return
		}
		_ = flapc.SendSNAC(wire.SNACFrame{FoodGroup: wire.Buddy, SubGroup: wire.BuddyDeparted, RequestID: wire.ReqIDFromServer},
			wire.SNAC_0x03_0x0C_BuddyDeparted{TLVUserInfo: wire.TLVUserInfo{ScreenName: "bob"}})
		_ = flapc.SendSNAC(wire.SNACFrame{FoodGroup: wire.OService, SubGroup: wire.OServiceUserInfoUpdate, RequestID: frame.RequestID},
			wire.SNAC_0x01_0x0F_OServiceUserInfoUpdate{UserInfo: []wire.TLVUserInfo{{ScreenName: "Alice"}}})

		// user info lookup: error reply
		frame, err = receiveFrame(flapc)
		if err != nil {
			return
		}
		_ = flapc.SendSNAC(wire.SNACFrame{FoodGroup: wire.Locate, SubGroup: wire.LocateErr, RequestID: frame.RequestID},
			wire.SNACError{Code: wire.ErrorCodeNotLoggedOn})

		// sign the user off for signing on elsewhere
		block := wire.TLVRestBlock{}
		block.Append(wire.NewTLVBE(0x0009, wire.OServiceDiscErrNewLogin))
		_ = flapc.NewSignoff(block)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := connectService(ctx, addr, []byte("cookie"), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	defer c.Close()

	reply, err := c.request(ctx, wire.OService, wire.OServiceUserInfoQuery, struct{}{})
	require.NoError(t, err)
	assert.Equal(t, wire.SNAC_0x01_0x0F_OServiceUserInfoUpdate{UserInfo: []wire.TLVUserInfo{{ScreenName: "Alice"}}}, reply.Body)

	select {
	case msg := <-c.Messages():
		assert.Equal(t, "bob", msg.Body.(wire.SNAC_0x03_0x0C_BuddyDeparted).ScreenName)
	case <-ctx.Done():
		t.Fatal("unsolicited SNAC not delivered")
	}

	_, err = c.request(ctx, wire.Locate, wire.LocateUserInfoQuery, wire.SNAC_0x02_0x05_LocateUserInfoQuery{ScreenName: "bob"})
	var snacErr *SNACError
	require.ErrorAs(t, err, &snacErr)
	assert.Equal(t, wire.ErrorCodeNotLoggedOn, snacErr.Code)

	select {
	case <-c.Closed():
	case <-ctx.Done():
		t.Fatal("connection didn't close on signoff")
	}
	var signoff *SignoffError
	require.ErrorAs(t, c.Err(), &signoff)
	assert.Equal(t, uint16(wire.OServiceDiscErrNewLogin), signoff.Code)
}

func TestSignoffErr(t *testing.T) {
	tests := []struct {
		name string
		tlvs []wire.TLV
		want *SignoffError
	}{
		{
			name: "no reason given",
			want: &SignoffError{},
		},
		{
			name: "uint8 code with more info URL",
			tlvs: []wire.TLV{
				wire.NewTLVBE(0x0009, uint8(0x01)),
				wire.NewTLVBE(0x000B, "https://example.com/why"),
			},
			want: &SignoffError{Code: 0x01, URL: "https://example.com/why"},
		},
		{
			name: "uint16 login error code",
			tlvs: []wire.TLV{wire.NewTLVBE(wire.LoginTLVTagsErrorSubcode, wire.LoginErrSuspendedAccount)},
			want: &SignoffError{Code: wire.LoginErrSuspendedAccount},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			require.NoError(t, wire.MarshalBE(wire.TLVRestBlock{TLVList: tt.tlvs}, buf))
			assert.Equal(t, tt.want, signoffErr(buf.Bytes()))
		})
	}
}

func TestDecodeRateClassIDs(t *testing.T) {
	// two classes; only the count and first ID are read
	body := []byte{0x00, 0x02, 0x00, 0x01, 0xFF, 0xFF}
	ids, err := decodeRateClassIDs(bytes.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, rateClassIDs{1, 2}, ids)
}

func TestConn_requestAll_multiPart(t *testing.T) {
	addr := fakeService(t, func(flapc *wire.FlapClient) {
		frame, err := receiveFrame(flapc)
		if err != nil {
			return
		}
		for i, name := range []string{"alice", "bob"} {
			flags := wire.SNACFlagsMoreToCome
			if i == 1 {
				flags = 0
			}
			_ = flapc.SendSNAC(wire.SNACFrame{FoodGroup: wire.Feedbag, SubGroup: wire.FeedbagReply, Flags: flags, RequestID: frame.RequestID},
				wire.SNAC_0x13_0x06_FeedbagReply{Items: []wire.FeedbagItem{{Name: name}}})
		}
		_, _ = receiveFrame(flapc) // wait for the client to hang up
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := connectService(ctx, addr, []byte("cookie"), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	defer c.Close()

	parts, err := c.requestAll(ctx, wire.Feedbag, wire.FeedbagQuery, struct{}{})
	require.NoError(t, err)
	require.Len(t, parts, 2)
	assert.Equal(t, "alice", parts[0].Body.(wire.SNAC_0x13_0x06_FeedbagReply).Items[0].Name)
	assert.Equal(t, "bob", parts[1].Body.(wire.SNAC_0x13_0x06_FeedbagReply).Items[0].Name)
}

func TestConnectService_TLS(t *testing.T) {
	cert, pool := selfSignedCert(t, "bos.example.com")
	addr := fakeServiceTLS(t, &tls.Config{Certificates: []tls.Certificate{cert}}, func(flapc *wire.FlapClient) {
		_, _ = receiveFrame(flapc) // wait for the client to hang up
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	tests := []struct {
		name    string
		tlsCfg  *tls.Config
		wantErr bool
	}{
		{
			name:   "trusted certificate",
			tlsCfg: &tls.Config{RootCAs: pool, ServerName: "bos.example.com"},
		},
		{
			name:    "certificate for another host",
			tlsCfg:  &tls.Config{RootCAs: pool, ServerName: "other.example.com"},
			wantErr: true,
		},
		{
			name:    "untrusted certificate",
			tlsCfg:  &tls.Config{ServerName: "bos.example.com"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := connectService(ctx, addr, []byte("cookie"), tt.tlsCfg, logger)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			c.Close()
		})
	}
}

func TestReferralTLS(t *testing.T) {
	tests := []struct {
		name    string
		base    *tls.Config
		tlvs    []wire.TLV
		want    *tls.Config
		wantErr error
	}{
		{
			name: "plaintext client ignores SSL state",
			tlvs: []wire.TLV{wire.NewTLVBE(wire.OServiceTLVTagsSSLState, wire.OServiceServiceResponseSSLStateResume)},
		},
		{
			name: "server reports SSL",
			base: &tls.Config{},
			tlvs: []wire.TLV{wire.NewTLVBE(wire.OServiceTLVTagsSSLState, wire.OServiceServiceResponseSSLStateResume)},
			want: &tls.Config{},
		},
		{
			name: "server doesn't report SSL state",
			base: &tls.Config{},
			want: &tls.Config{},
		},
		{
			name: "server names the certificate",
			base: &tls.Config{},
			tlvs: []wire.TLV{wire.NewTLVBE(wire.OServiceTLVTagsSSLCertName, "bos.example.com")},
			want: &tls.Config{ServerName: "bos.example.com"},
		},
		{
			name: "configured server name wins",
			base: &tls.Config{ServerName: "mine.example.com"},
			tlvs: []wire.TLV{wire.NewTLVBE(wire.OServiceTLVTagsSSLCertName, "bos.example.com")},
			want: &tls.Config{ServerName: "mine.example.com"},
		},
		{
			name:    "no plaintext downgrade",
			base:    &tls.Config{},
			tlvs:    []wire.TLV{wire.NewTLVBE(wire.OServiceTLVTagsSSLState, wire.OServiceServiceResponseSSLStateNotUsed)},
			wantErr: ErrSSLUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := referralTLS(tt.base, wire.TLVRestBlock{TLVList: tt.tlvs})
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConnectService_TLSToPlaintextPort(t *testing.T) {
	addr := fakeService(t, func(flapc *wire.FlapClient) {})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := connectService(ctx, addr, []byte("cookie"), &tls.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	assert.ErrorIs(t, err, ErrNotTLS)
}
