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
	username string
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

type EventCallback func(c *Client, msg map[string]interface{})

var eventCallbacks = make(map[string][]EventCallback)

func on(event string, callback EventCallback) {
	eventCallbacks[event] = append(eventCallbacks[event], callback)
}

func handleJoinEvent(c *Client, msg map[string]interface{}) {
	room, ok := msg["room"].(string)
	if !ok || room == "" {
		return
	}

	username, ok := msg["username"].(string)
	if !ok || username == "" {
		return
	}

	mutex.Lock()
	defer mutex.Unlock()

	leaveRoom(c, c.room)
	joinRoom(c, room, username)

	// Send joining message to room
	message, _ := json.Marshal(map[string]interface{}{
		"username": "Server",
		"type":     "join",
		"message":  fmt.Sprintf("%s joined the room", username),
	})

	toRoom(room, message)
}

func handleLeaveEvent(c *Client, msg map[string]interface{}) {
	room, ok := msg["room"].(string)
	if !ok || room == "" {
		return
	}

	username, ok := msg["username"].(string)
	if !ok || username == "" {
		return
	}

	mutex.Lock()
	defer mutex.Unlock()

	leaveRoom(c, room)
	handleLeave(room, username)

	//if at least one client is left in the room, send leaving message
	if _, ok := rooms[room]; !ok {
		return
	}

	// Send leaving message to room
	message, _ := json.Marshal(map[string]interface{}{
		"username": "Server",
		"type":     "leave",
		"message":  fmt.Sprintf("%s left the room", username),
	})

	toRoom(room, message)
}

func handleMessageEvent(c *Client, msg map[string]interface{}) {
	room, ok := msg["room"].(string)
	if !ok || room == "" {
		return
	}
	message, ok := msg["message"].(string)
	if !ok || message == "" {
		return
	}
	username, ok := msg["username"].(string)
	if !ok || username == "" {
		return
	}
	// to JSON
	message = fmt.Sprintf(`{"username":"%s","message":"%s"}`, username, message)
	toRoom(room, []byte(message))
}

// init function runs before main
func init() {
	on("join", handleJoinEvent)
	on("leave", handleLeaveEvent)
	on("message", handleMessageEvent)
}

func main() {
	fs := http.FileServer(http.Dir("client"))

	http.HandleFunc("/ws", handleConnections)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fs.ServeHTTP(w, r)
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
	handleLeave(c.room, c.username)
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
		callbacks, ok := eventCallbacks[action]
		if !ok {
			continue
		}
		for _, callback := range callbacks {
			callback(c, msg)
		}
	}
}


func handleLeave(room string, username string) {
	// if any clients are left in the room, send leaving message
	if _, ok := rooms[room]; ok {
		message, _ := json.Marshal(map[string]interface{}{
			"username": "Server",
			"type":     "leave",
			"message":  fmt.Sprintf("%s left the room", username),
		})

		toRoom(room, message)
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

func joinRoom(c *Client, roomName string, username string) {
	createRoom(roomName)
	rooms[roomName][c] = true
	c.room = roomName
	c.username = username
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