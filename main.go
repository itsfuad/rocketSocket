package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
)

type Client struct {
	conn *Conn
	send chan []byte
	room string
}

type Conn struct {
	conn net.Conn
	buf  *bufio.ReadWriter
}

func (c *Conn) Close() error {
	return c.conn.Close()
}

var clients = make(map[*Client]bool)
var rooms = make(map[string]map[*Client]bool)
var broadcast = make(chan []byte)
var mutex = &sync.Mutex{}

func main() {
	http.HandleFunc("/ws", handleConnections)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "index.html")
	})

	go handleMessages()

	fmt.Println("Server started on :8080")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		fmt.Println("Error starting server:", err)
	}
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	conn, err := upgradeHTTPToWebSocket(w, r)
	if err != nil {
		fmt.Println("Error upgrading connection:", err)
		return
	}
	defer conn.Close()

	client := &Client{conn: conn, send: make(chan []byte, 256)}
	mutex.Lock()
	clients[client] = true
	mutex.Unlock()

	go client.writePump()
	client.readPump()
}

func upgradeHTTPToWebSocket(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("webserver doesn't support hijacking")
	}

	conn, _, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("error hijacking connection: %v", err)
	}

	key := r.Header.Get("Sec-WebSocket-Key")
	accept := makeAcceptKey(key)

	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"

	if _, err := conn.Write([]byte(response)); err != nil {
		return nil, fmt.Errorf("error writing to connection: %v", err)
	}

	return &Conn{conn: conn, buf: bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))}, nil
}

func makeAcceptKey(key string) string {
	h := sha1.New()
	h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")) // Magic string
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func cleanUp(c *Client) {
	mutex.Lock()
	delete(clients, c)
	leaveRoom(c, c.room)
	mutex.Unlock()
	c.conn.conn.Close()
}

func (c *Client) readPump() {
	defer cleanUp(c)
	for {
		_, message, err := readMessage(c.conn)
		if err != nil {
			break
		}
		var msg map[string]interface{}
		if err := json.Unmarshal(message, &msg); err != nil {
			fmt.Println("Error unmarshaling message:", err)
			continue
		}
		action, ok := msg["action"].(string)
		if !ok {
			continue
		}
		switch action {
		case "join":
			room, ok := msg["room"].(string)
			if !ok {
				continue
			}
			mutex.Lock()
			leaveRoom(c, c.room)
			joinRoom(c, room)
			mutex.Unlock()
		case "leave":
			room, ok := msg["room"].(string)
			if !ok {
				continue
			}
			mutex.Lock()
			leaveRoom(c, room)
			mutex.Unlock()
		case "message":
			room, ok := msg["room"].(string)
			if !ok {
				continue
			}
			message, ok := msg["message"].(string)
			if !ok {
				continue
			}
			username, ok := msg["username"].(string)
			if !ok {
				username = "Anonymous"
			}
			//to JSON
			message = fmt.Sprintf(`{"username":"%s","message":"%s"}`, username, message)
			toRoom(room, []byte(message))
		}
	}
}

func (c *Client) writePump() {
	for message := range c.send {
		if err := writeMessage(c.conn, 1, message); err != nil {
			break
		}
	}
}

func handleMessages() {
	for {
		message := <-broadcast
		mutex.Lock()
		for client := range clients {
			select {
			case client.send <- message:
			default:
				close(client.send)
				delete(clients, client)
			}
		}
		mutex.Unlock()
	}
}

func createRoom(roomName string) {
	if rooms[roomName] == nil {
		rooms[roomName] = make(map[*Client]bool)
	}
}

func joinRoom(c *Client, roomName string) {
	createRoom(roomName)
	rooms[roomName][c] = true
	c.room = roomName
}

func leaveRoom(c *Client, roomName string) {
	if _, ok := rooms[roomName]; ok {
		delete(rooms[roomName], c)
		if len(rooms[roomName]) == 0 {
			delete(rooms, roomName)
		}
	}
}

func toRoom(roomName string, message []byte) {
	if room, ok := rooms[roomName]; ok {
		for client := range room {
			client.send <- message
		}
	}
}

func readMessage(conn *Conn) (opcode int, message []byte, err error) {
	var header [2]byte
	if _, err := conn.buf.Read(header[:]); err != nil {
		return 0, nil, err
	}
	fin := header[0]&0x80 != 0
	opcode = int(header[0] & 0x0F)
	masked := header[1]&0x80 != 0
	payloadLen := int(header[1] & 0x7F)

	if payloadLen == 126 {
		var extended [2]byte
		if _, err := conn.buf.Read(extended[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = int(extended[0])<<8 | int(extended[1])
	} else if payloadLen == 127 {
		var extended [8]byte
		if _, err := conn.buf.Read(extended[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = int(extended[0])<<56 | int(extended[1])<<48 | int(extended[2])<<40 | int(extended[3])<<32 |
			int(extended[4])<<24 | int(extended[5])<<16 | int(extended[6])<<8 | int(extended[7])
	}

	maskKey := make([]byte, 4)
	if masked {
		if _, err := conn.buf.Read(maskKey); err != nil {
			return 0, nil, err
		}
	}

	message = make([]byte, payloadLen)
	if _, err := conn.buf.Read(message); err != nil {
		return 0, nil, err
	}

	if masked {
		for i := 0; i < payloadLen; i++ {
			message[i] ^= maskKey[i%4]
		}
	}

	if !fin {
		return 0, nil, fmt.Errorf("fragmented messages are not supported")
	}

	return opcode, message, nil
}

// writeMessage writes a message to the connection
func writeMessage(conn *Conn, opcode int, message []byte) error {
	var header [2]byte
	header[0] = 0x80 | byte(opcode) // FIN bit set and opcode
	payloadLen := len(message)

	if payloadLen <= 125 {
		header[1] = byte(payloadLen)
		if _, err := conn.buf.Write(header[:]); err != nil {
			return err
		}
	} else if payloadLen <= 65535 {
		header[1] = 126
		if _, err := conn.buf.Write(header[:]); err != nil {
			return err
		}
		var extended [2]byte
		extended[0] = byte(payloadLen >> 8)
		extended[1] = byte(payloadLen)
		if _, err := conn.buf.Write(extended[:]); err != nil {
			return err
		}
	} else {
		header[1] = 127
		if _, err := conn.buf.Write(header[:]); err != nil {
			return err
		}
		var extended [8]byte
		extended[0] = byte(payloadLen >> 56)
		extended[1] = byte(payloadLen >> 48)
		extended[2] = byte(payloadLen >> 40)
		extended[3] = byte(payloadLen >> 32)
		extended[4] = byte(payloadLen >> 24)
		extended[5] = byte(payloadLen >> 16)
		extended[6] = byte(payloadLen >> 8)
		extended[7] = byte(payloadLen)
		if _, err := conn.buf.Write(extended[:]); err != nil {
			return err
		}
	}

	if _, err := conn.buf.Write(message); err != nil {
		return err
	}
	return conn.buf.Flush()
}