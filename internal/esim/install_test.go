package esim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/damonto/euicc-go/driver"
)

const smdpHost = "smdp.example.test"

var transaction = []byte{1, 2, 3, 4, 5, 6, 7, 8}

func ber(tag string, parts ...[]byte) []byte {
	b, _ := hex.DecodeString(tag)
	v := bytes.Join(parts, nil)
	switch n := len(v); {
	case n < 0x80:
		b = append(b, byte(n))
	case n < 0x100:
		b = append(b, 0x81, byte(n))
	default:
		b = append(b, 0x82, byte(n>>8), byte(n))
	}
	return append(b, v...)
}

func installed() []byte {
	return ber("bf37",
		ber("bf27",
			ber("80", transaction),
			ber("bf2f", ber("80", []byte{7}), ber("81", []byte{4, 0x80}), ber("0c", []byte(smdpHost))),
			ber("a2", ber("a0", ber("04", []byte{0})))),
		ber("5f37", []byte{9}))
}

type euicc struct {
	bound   []byte
	block   []byte
	loading []byte
	hash    []byte
	match   bool
	loaded  bool
	sent    bool
	open    int
}

func (c *euicc) Connect() error    { return nil }
func (c *euicc) Disconnect() error { return nil }

func (c *euicc) OpenLogicalChannel([]byte) (byte, error) {
	c.open++
	return 1, nil
}

func (c *euicc) CloseLogicalChannel(byte) error {
	c.open--
	return nil
}

func (c *euicc) Transmit(cmd []byte) ([]byte, error) {
	if len(cmd) < 5 || cmd[0] != 0x81 || cmd[1] != 0xe2 || len(cmd) < 5+int(cmd[4]) {
		return nil, fmt.Errorf("unexpected command % x", cmd)
	}
	c.block = append(c.block, cmd[5:5+int(cmd[4])]...)
	if cmd[2] != 0x91 {
		return []byte{0x90, 0}, nil
	}
	req := c.block
	c.block = nil
	out, err := c.answer(req)
	return append(out, 0x90, 0), err
}

func (c *euicc) answer(req []byte) ([]byte, error) {
	if len(c.loading) > 0 || bytes.HasPrefix(req, []byte{0xbf, 0x36}) {
		c.loading = append(c.loading, req...)
		if len(c.loading) < len(c.bound) {
			return nil, nil
		}
		if !bytes.Equal(c.loading, c.bound) {
			return nil, errors.New("the package arrived altered")
		}
		c.loading = nil
		c.loaded = true
		return installed(), nil
	}
	switch req[1] {
	case 0x2e:
		return ber("bf2e", ber("80", bytes.Repeat([]byte{5}, 16))), nil
	case 0x20:
		return ber("bf20", ber("82", []byte{2, 5, 0})), nil
	case 0x38:
		c.match = bytes.Contains(req, []byte("MATCH-1"))
		return ber("bf38", ber("a0", ber("80", transaction))), nil
	case 0x21:
		if i := bytes.Index(req, []byte{0x04, sha256.Size}); i >= 0 {
			c.hash = slices.Clone(req[i+2 : i+2+sha256.Size])
		}
		return ber("bf21", ber("a0", ber("80", transaction))), nil
	case 0x41:
		return ber("bf41", ber("a0", ber("80", transaction))), nil
	case 0x28:
		if c.loaded && !c.sent {
			return ber("bf28", ber("a0", ber("bf2f", ber("80", []byte{7}), ber("81", []byte{4, 0x80}), ber("0c", []byte(smdpHost))))), nil
		}
		return ber("bf28", ber("a0")), nil
	case 0x2b:
		return ber("bf2b", ber("a0", installed())), nil
	case 0x30:
		c.sent = true
		return ber("bf30", ber("80", []byte{0})), nil
	}
	return nil, fmt.Errorf("unexpected request % x", req)
}

type smdp struct {
	bound        []byte
	confirmation bool
	refuse       bool
	calls        chan string
}

func (s *smdp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	call := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	s.calls <- call
	if r.Host != smdpHost || r.Header.Get("User-Agent") != "gsma-rsp-lpad" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	b64 := base64.StdEncoding.EncodeToString
	out := map[string]any{
		"header":        map[string]any{"functionExecutionStatus": map[string]any{"status": "Executed-Success"}},
		"transactionId": hex.EncodeToString(transaction),
	}
	switch call {
	case "initiateAuthentication":
		out["serverSigned1"] = b64(ber("30", ber("80", transaction)))
		out["serverSignature1"] = b64(ber("5f37", []byte{1}))
		out["euiccCiPKIdToBeUsed"] = b64(ber("04", []byte{2}))
		out["serverCertificate"] = b64(ber("30", ber("02", []byte{3})))
	case "authenticateClient":
		if s.refuse {
			out["header"] = map[string]any{"functionExecutionStatus": map[string]any{
				"status":         "Failed",
				"statusCodeData": map[string]any{"message": "code already used"},
			}}
			break
		}
		var required byte
		if s.confirmation {
			required = 0xff
		}
		iccid, _ := hex.DecodeString("988802010000000000f1")
		out["profileMetadata"] = b64(ber("bf25", ber("5a", iccid), ber("91", []byte("Carrier")), ber("92", []byte("Plan"))))
		out["smdpSigned2"] = b64(ber("30", ber("80", transaction), ber("01", []byte{required})))
		out["smdpSignature2"] = b64(ber("5f37", []byte{4}))
		out["smdpCertificate"] = b64(ber("30", ber("02", []byte{5})))
	case "getBoundProfilePackage":
		out["boundProfilePackage"] = b64(s.bound)
	case "cancelSession":
	case "handleNotification":
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (s *smdp) seen() []string {
	var out []string
	for {
		select {
		case c := <-s.calls:
			out = append(out, c)
		default:
			return out
		}
	}
}

func carrier(t *testing.T, trusted bool) (*Manager, *euicc, *smdp) {
	t.Helper()
	bound := ber("bf36",
		ber("bf23", ber("82", []byte{1})),
		ber("a0", ber("87", bytes.Repeat([]byte{0x11}, 40))),
		ber("a1", ber("88", bytes.Repeat([]byte{0x22}, 90))),
		ber("a3", ber("86", bytes.Repeat([]byte{0x33}, 700)), ber("86", bytes.Repeat([]byte{0x44}, 300))))
	s := &smdp{bound: bound, calls: make(chan string, 16)}
	srv := httptest.NewTLSServer(s)
	t.Cleanup(srv.Close)

	was := http.DefaultTransport
	tr := was.(*http.Transport).Clone()
	tr.Proxy = nil
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return new(net.Dialer).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	tr.DialContext = dial
	if trusted {
		local := srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
		local.ServerName = "example.com"
		tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := dial(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return tls.Client(c, local), nil
		}
	}
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = was })

	c := &euicc{bound: bound}
	m := New(
		func() driver.SmartCardChannel { return c },
		func() (string, error) { return strings.Repeat("0", 15), nil },
		func() {},
	)
	return m, c, s
}

func TestAProfileIsInstalledFromItsCode(t *testing.T) {
	m, c, s := carrier(t, true)
	if err := m.Install("LPA:1$"+smdpHost+"$MATCH-1", ""); err != nil {
		t.Fatal(err)
	}
	want := []string{"initiateAuthentication", "authenticateClient", "getBoundProfilePackage", "handleNotification"}
	if got := s.seen(); !slices.Equal(got, want) {
		t.Fatalf("the carrier saw %v", got)
	}
	if !c.match || !c.loaded || !c.sent || c.hash != nil || c.open != 0 {
		t.Fatalf("chip: match=%v loaded=%v sent=%v hash=%x open=%d", c.match, c.loaded, c.sent, c.hash, c.open)
	}
}

func TestTheConfirmationCodeReachesTheChip(t *testing.T) {
	m, c, s := carrier(t, true)
	s.confirmation = true
	if err := m.Install("1$"+smdpHost+"$MATCH-1", "4321"); err != nil {
		t.Fatal(err)
	}
	inner := sha256.Sum256([]byte("4321"))
	want := sha256.Sum256(append(inner[:], transaction...))
	if !c.loaded || !bytes.Equal(c.hash, want[:]) {
		t.Fatalf("loaded=%v hash=%x", c.loaded, c.hash)
	}
}

func TestAMissingConfirmationCodeCancelsTheDownload(t *testing.T) {
	m, c, s := carrier(t, true)
	s.confirmation = true
	if err := m.Install("1$"+smdpHost+"$MATCH-1", ""); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("got %v", err)
	}
	if got := s.seen(); !slices.Equal(got, []string{"initiateAuthentication", "authenticateClient", "cancelSession"}) {
		t.Fatalf("the carrier saw %v", got)
	}
	if c.loaded || c.open != 0 {
		t.Fatalf("loaded=%v open=%d", c.loaded, c.open)
	}
}

func TestARefusedCodeInstallsNothing(t *testing.T) {
	m, c, s := carrier(t, true)
	s.refuse = true
	if err := m.Install("1$"+smdpHost+"$MATCH-1", ""); err == nil || !strings.Contains(err.Error(), "code already used") {
		t.Fatalf("got %v", err)
	}
	if c.loaded || c.open != 0 {
		t.Fatalf("loaded=%v open=%d", c.loaded, c.open)
	}
}

func TestAnUntrustedServerGetsNothing(t *testing.T) {
	m, c, s := carrier(t, false)
	if err := m.Install("1$"+smdpHost+"$MATCH-1", ""); err == nil {
		t.Fatal("installed from an untrusted server")
	}
	if got := s.seen(); len(got) != 0 || c.loaded {
		t.Fatalf("the untrusted server saw %v", got)
	}
}
