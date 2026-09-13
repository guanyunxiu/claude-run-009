package ws

import (
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Client 单条 WebSocket 连接。
type Client struct {
	conn *websocket.Conn
	send chan []byte

	// closeMu 保护 send 的发送与关闭，消除 fanout 与 Unsubscribe 之间
	// “send on closed channel”的竞态。
	closeMu sync.RWMutex
	closed  bool
}

// deliver 非阻塞投递一条消息；返回 false 表示缓冲已满或连接已关闭。
func (c *Client) deliver(payload []byte) bool {
	c.closeMu.RLock()
	defer c.closeMu.RUnlock()
	if c.closed {
		return false
	}
	select {
	case c.send <- payload:
		return true
	default:
		return false
	}
}

// shutdown 幂等关闭发送通道（最多关闭一次）。
func (c *Client) shutdown() {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.send)
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(_ *http.Request) bool { return true },
}

// HandleWS 处理 GET /api/v1/sessions/:id/subtitles/ws
// 查询参数 replay=<n> 表示连接建立时回放最近 n 条 final 字幕。
func (h *Hub) HandleWS(ctx *gin.Context) {
	sessionID := ctx.Param("id")

	conn, err := upgrader.Upgrade(ctx.Writer, ctx.Request, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}

	client := &Client{
		conn: conn,
		send: make(chan []byte, sendBufferSize),
	}
	h.Subscribe(sessionID, client)

	// 先回放历史，再投递实时消息。
	replayCount, _ := strconv.ParseInt(ctx.DefaultQuery("replay", "20"), 10, 64)
	if replayCount > 0 {
		if history, err := h.Replay(ctx.Request.Context(), sessionID, replayCount); err == nil {
			for _, payload := range history {
				client.deliver(payload)
			}
		}
	}

	go client.writePump()
	go client.readPump(h, sessionID)
}

func (c *Client) readPump(h *Hub, sessionID string) {
	defer func() {
		h.Unsubscribe(sessionID, c)
		_ = c.conn.Close()
	}()

	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	// 服务端不处理业务消息；读取客户端 ping/close 帧以感知断线。
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			break
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case payload, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
