package sms

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

const gsmBasic = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞ\x1bÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"

var (
	basic    = []rune(gsmBasic)
	gsmExt   = map[byte]rune{10: '\f', 20: '^', 40: '{', 41: '}', 47: '\\', 60: '[', 61: '~', 62: ']', 64: '|', 101: '€'}
	encTable = map[rune][]byte{}
)

func init() {
	for i, c := range basic {
		if i != 27 {
			encTable[c] = []byte{byte(i)}
		}
	}
	for i, c := range gsmExt {
		encTable[c] = []byte{27, i}
	}
}

const WAPPushPort = 2948

var (
	spaces   = regexp.MustCompile(`[\s().-]`)
	numberRe = regexp.MustCompile(`^\+?[0-9]{2,20}$`)
)

func Number(v string) (string, error) {
	v = spaces.ReplaceAllString(v, "")
	if strings.HasPrefix(v, "00") && len(v) > 4 {
		v = "+" + v[2:]
	}
	if !numberRe.MatchString(v) {
		return "", fmt.Errorf("%q is not a phone number", v)
	}
	return v, nil
}

func pack7(values []byte, header []byte) (int, []byte) {
	skip := (len(header)*8 + 6) / 7
	count := skip + len(values)
	out := make([]byte, (count*7+7)/8)
	copy(out, header)
	for i, v := range values {
		bit := (i + skip) * 7
		for b := 0; b < 7; b++ {
			if v&(1<<b) != 0 {
				out[(bit+b)/8] |= 1 << ((bit + b) % 8)
			}
		}
	}
	return count, out
}

func unpack7(data []byte, count, skip int) (string, error) {
	if (count*7+7)/8 > len(data) {
		return "", errors.New("truncated GSM text")
	}
	var b strings.Builder
	escape := false
	for i := skip; i < count; i++ {
		var v byte
		for k := 0; k < 7; k++ {
			bit := i*7 + k
			if data[bit/8]&(1<<(bit%8)) != 0 {
				v |= 1 << k
			}
		}
		switch {
		case escape:
			if c, ok := gsmExt[v]; ok {
				b.WriteRune(c)
			} else {
				b.WriteRune('�')
			}
			escape = false
		case v == 27:
			escape = true
		default:
			b.WriteRune(basic[v])
		}
	}
	return b.String(), nil
}

func address(v string) []byte {
	digits := strings.TrimPrefix(v, "+")
	toa := byte(0x81)
	if strings.HasPrefix(v, "+") {
		toa = 0x91
	}
	padded := digits
	if len(padded)%2 == 1 {
		padded += "F"
	}
	out := []byte{byte(len(digits)), toa}
	for i := 0; i < len(padded); i += 2 {
		lo := hexval(padded[i])
		hi := hexval(padded[i+1])
		out = append(out, hi<<4|lo)
	}
	return out
}

func hexval(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c == 'F' || c == 'f':
		return 15
	}
	return 0
}

func Encodable7(text string) bool {
	for _, c := range text {
		if _, ok := encTable[c]; !ok {
			return false
		}
	}
	return true
}

func Submit(destination, text string) ([][]byte, error) {
	dest, err := Number(destination)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" || len([]rune(text)) > 1600 {
		return nil, errors.New("a message has 1 to 1600 characters")
	}
	gsm := Encodable7(text)
	var units [][]byte
	for _, c := range text {
		if gsm {
			units = append(units, encTable[c])
			continue
		}
		var u []byte
		for _, w := range utf16.Encode([]rune{c}) {
			u = append(u, byte(w>>8), byte(w))
		}
		units = append(units, u)
	}
	single, limit := 140, 134
	if gsm {
		single, limit = 160, 153
	}
	total := 0
	for _, u := range units {
		total += len(u)
	}
	if total <= single {
		limit = single
	}
	parts := [][]byte{{}}
	for _, u := range units {
		if len(parts[len(parts)-1])+len(u) > limit {
			parts = append(parts, []byte{})
		}
		parts[len(parts)-1] = append(parts[len(parts)-1], u...)
	}
	var ref [1]byte
	rand.Read(ref[:])
	var out [][]byte
	for i, part := range parts {
		var header []byte
		if len(parts) > 1 {
			header = []byte{5, 0, 3, ref[0], byte(len(parts)), byte(i + 1)}
		}
		first := byte(1)
		if header != nil {
			first = 0x41
		}
		var length int
		var body []byte
		dcs := byte(8)
		if gsm {
			dcs = 0
			length, body = pack7(part, header)
		} else {
			body = append(append([]byte{}, header...), part...)
			length = len(body)
		}
		pdu := append([]byte{first, 0}, address(dest)...)
		pdu = append(pdu, 0, dcs, byte(length))
		out = append(out, append(pdu, body...))
	}
	return out, nil
}

type Concat struct {
	Ref, Total, Seq int
}

type Message struct {
	Number    string
	Text      string
	Timestamp time.Time
	Concat    *Concat
	Port      int
	Data      []byte
	Class     int
}

func bcd(x byte) int { return int(x&15)*10 + int(x>>4) }

func Deliver(pdu []byte) (*Message, error) {
	if len(pdu) < 3 || pdu[0]&3 != 0 {
		return nil, errors.New("not an SMS delivery")
	}
	first, n, toa := pdu[0], int(pdu[1]), pdu[2]
	pos := 3
	size := (n + 1) / 2
	if pos+size+10 > len(pdu) {
		return nil, errors.New("truncated SMS delivery")
	}
	raw := pdu[pos : pos+size]
	pos += size
	m := &Message{Class: -1}
	if toa&0x70 == 0x50 {
		s, err := unpack7(raw, n*4/7, 0)
		if err != nil {
			return nil, err
		}
		m.Number = s
	} else {
		var b strings.Builder
		for _, x := range raw {
			b.WriteByte("0123456789*#abcf"[x&15])
			b.WriteByte("0123456789*#abcf"[x>>4])
		}
		s := b.String()
		if len(s) > n {
			s = s[:n]
		}
		if toa&0x70 == 0x10 {
			s = "+" + s
		}
		m.Number = s
	}
	dcs := pdu[pos+1]
	pos += 2
	st := pdu[pos : pos+7]
	pos += 7
	zone := time.Duration(bcd(st[6]&0xf7)*15) * time.Minute
	if st[6]&8 != 0 {
		zone = -zone
	}
	year := bcd(st[0])
	if year < 90 {
		year += 2000
	} else {
		year += 1900
	}
	m.Timestamp = time.Date(year, time.Month(bcd(st[1])), bcd(st[2]), bcd(st[3]), bcd(st[4]), bcd(st[5]), 0, time.FixedZone("", int(zone.Seconds())))
	if bcd(st[1]) < 1 || bcd(st[1]) > 12 || bcd(st[2]) < 1 || bcd(st[2]) > 31 {
		m.Timestamp = time.Now()
	}
	length := int(pdu[pos])
	data := pdu[pos+1:]
	header := 0
	if first&0x40 != 0 {
		if len(data) == 0 {
			return nil, errors.New("missing SMS header")
		}
		header = int(data[0]) + 1
		if header > len(data) {
			return nil, errors.New("truncated SMS header")
		}
		for i := 1; i < header; {
			if i+2 > header {
				return nil, errors.New("malformed SMS header")
			}
			tag, sz := data[i], int(data[i+1])
			if i+2+sz > header {
				return nil, errors.New("malformed SMS header")
			}
			v := data[i+2 : i+2+sz]
			switch {
			case tag == 0 && sz == 3:
				m.Concat = &Concat{int(v[0]), int(v[1]), int(v[2])}
			case tag == 8 && sz == 4:
				m.Concat = &Concat{int(v[0])<<8 | int(v[1]), int(v[2]), int(v[3])}
			case tag == 5 && sz == 4:
				m.Port = int(v[0])<<8 | int(v[1])
			case tag == 4 && sz == 2:
				m.Port = int(v[0])
			}
			i += 2 + sz
		}
		if c := m.Concat; c != nil && (c.Seq < 1 || c.Seq > c.Total || c.Total > 32) {
			return nil, errors.New("invalid SMS part index")
		}
	}
	alphabet := 0
	switch {
	case dcs&0xc0 == 0:
		alphabet = int(dcs>>2) & 3
		if dcs&0x10 != 0 {
			m.Class = int(dcs & 3)
		}
		if dcs&0x20 != 0 {
			alphabet = 1
		}
	case dcs&0xf0 == 0xc0 || dcs&0xf0 == 0xd0:
		alphabet = 0
	case dcs&0xf0 == 0xe0:
		alphabet = 2
	case dcs&0xf0 == 0xf0:
		if dcs&4 != 0 {
			alphabet = 1
		}
		m.Class = int(dcs & 3)
	default:
		alphabet = 1
	}
	switch alphabet {
	case 0:
		s, err := unpack7(data, length, (header*8+6)/7)
		if err != nil {
			return nil, err
		}
		m.Text = s
	case 2:
		if length > len(data) || length < header {
			return nil, errors.New("truncated Unicode SMS")
		}
		u := data[header:length]
		var w []uint16
		for i := 0; i+1 < len(u); i += 2 {
			w = append(w, uint16(u[i])<<8|uint16(u[i+1]))
		}
		m.Text = string(utf16.Decode(w))
	default:
		if length > len(data) || length < header {
			return nil, errors.New("truncated binary SMS")
		}
		m.Data = append([]byte(nil), data[header:length]...)
	}
	return m, nil
}

func Received(data []byte) (*Message, error) {
	if len(data) == 0 || data[0] > 12 || len(data) <= 1+int(data[0]) {
		return nil, errors.New("invalid service-centre prefix")
	}
	return Deliver(data[1+int(data[0]):])
}
