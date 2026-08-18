package mc

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Minecraft Server List Ping (the "status" handshake every server browser
// uses). Implemented directly over TCP: connect, send a handshake with
// next-state=1, send a status request, read the JSON response.
// Reference: the modern (1.7+) protocol, which every current server speaks.

type PingPlayer struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

type PingResult struct {
	Online      bool         `json:"online"`
	Version     string       `json:"version,omitempty"`
	Protocol    int          `json:"protocol,omitempty"`
	MOTD        string       `json:"motd,omitempty"`
	PlayersNow  int          `json:"players_now"`
	PlayersMax  int          `json:"players_max"`
	Sample      []PingPlayer `json:"sample,omitempty"`
	FaviconPNG  string       `json:"favicon,omitempty"` // data: URI as served by the server
	LatencyMS   int64        `json:"latency_ms,omitempty"`
	Error       string       `json:"error,omitempty"`
}

func writeVarInt(w io.Writer, v int32) error {
	uv := uint32(v)
	var buf [5]byte
	n := 0
	for {
		b := byte(uv & 0x7f)
		uv >>= 7
		if uv != 0 {
			b |= 0x80
		}
		buf[n] = b
		n++
		if uv == 0 {
			break
		}
	}
	_, err := w.Write(buf[:n])
	return err
}

func readVarInt(r io.ByteReader) (int32, error) {
	var result uint32
	for i := 0; i < 5; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		result |= uint32(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return int32(result), nil
		}
	}
	return 0, errors.New("varint too long")
}

func writeString(w io.Writer, s string) error {
	if err := writeVarInt(w, int32(len(s))); err != nil {
		return err
	}
	_, err := io.WriteString(w, s)
	return err
}

// packet builds a length-prefixed packet with the given id and payload.
func packet(id int32, payload []byte) []byte {
	var body []byte
	var idBuf strings.Builder
	writeVarInt(&idBuf, id)
	body = append(body, idBuf.String()...)
	body = append(body, payload...)

	var out strings.Builder
	writeVarInt(&out, int32(len(body)))
	return append([]byte(out.String()), body...)
}

type statusResponse struct {
	Version struct {
		Name     string `json:"name"`
		Protocol int    `json:"protocol"`
	} `json:"version"`
	Players struct {
		Max    int          `json:"max"`
		Online int          `json:"online"`
		Sample []PingPlayer `json:"sample"`
	} `json:"players"`
	Description json.RawMessage `json:"description"`
	Favicon     string          `json:"favicon"`
}

// Ping queries a running server for its status. Always returns a result; the
// Error field is set when the server could not be reached.
func Ping(host string, port int, timeout time.Duration) PingResult {
	if host == "" {
		host = "127.0.0.1"
	}
	start := time.Now()
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return PingResult{Error: "not reachable"}
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))

	// Handshake: protocol version -1 (unknown/any), host, port, next state 1.
	var hs []byte
	{
		var b strings.Builder
		writeVarInt(&b, -1)
		writeString(&b, host)
		binary.Write(&b, binary.BigEndian, uint16(port))
		writeVarInt(&b, 1)
		hs = []byte(b.String())
	}
	if _, err := conn.Write(packet(0x00, hs)); err != nil {
		return PingResult{Error: "handshake failed"}
	}
	if _, err := conn.Write(packet(0x00, nil)); err != nil { // status request
		return PingResult{Error: "status request failed"}
	}

	br := bufio.NewReader(conn)
	length, err := readVarInt(br)
	if err != nil || length <= 0 || length > 4<<20 {
		return PingResult{Error: "bad response"}
	}
	if _, err := readVarInt(br); err != nil { // packet id
		return PingResult{Error: "bad response"}
	}
	strLen, err := readVarInt(br)
	if err != nil || strLen < 0 || strLen > 4<<20 {
		return PingResult{Error: "bad response"}
	}
	payload := make([]byte, strLen)
	if _, err := io.ReadFull(br, payload); err != nil {
		return PingResult{Error: "truncated response"}
	}

	var sr statusResponse
	if err := json.Unmarshal(payload, &sr); err != nil {
		return PingResult{Error: "unparsable response"}
	}
	return PingResult{
		Online:     true,
		Version:    sr.Version.Name,
		Protocol:   sr.Version.Protocol,
		MOTD:       flattenMOTD(sr.Description),
		PlayersNow: sr.Players.Online,
		PlayersMax: sr.Players.Max,
		Sample:     sr.Players.Sample,
		FaviconPNG: sr.Favicon,
		LatencyMS:  time.Since(start).Milliseconds(),
	}
}

// flattenMOTD renders the description, which may be a plain string or a
// nested chat-component object, into readable text with formatting codes
// stripped.
func flattenMOTD(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return stripFormatting(s)
	}
	var comp chatComponent
	if json.Unmarshal(raw, &comp) == nil {
		return stripFormatting(comp.flatten())
	}
	return ""
}

type chatComponent struct {
	Text  string          `json:"text"`
	Extra []chatComponent `json:"extra"`
}

func (c chatComponent) flatten() string {
	var b strings.Builder
	b.WriteString(c.Text)
	for _, e := range c.Extra {
		b.WriteString(e.flatten())
	}
	return b.String()
}

// stripFormatting removes legacy section-sign colour codes.
func stripFormatting(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '§' && i+1 < len(runes) {
			i++ // skip the code character too
			continue
		}
		b.WriteRune(runes[i])
	}
	return strings.TrimSpace(b.String())
}
