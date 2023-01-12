package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/olahol/melody"

	log "maunium.net/go/maulogger/v2"
)

var (
	errMissingToken = Error{
		HTTPStatus: http.StatusForbidden,
		ErrorCode:  "M_MISSING_TOKEN",
		Message:    "Missing authorization header",
	}
	errUnknownToken = Error{
		HTTPStatus: http.StatusForbidden,
		ErrorCode:  "M_UNKNOWN_TOKEN",
		Message:    "Unknown authorization token",
	}

	ErrWebsocketNotConnected = errors.New("websocket not connected")
	ErrWebsocketClosed       = errors.New("websocket closed before response received")
)

type WechatService struct {
	log log.Logger

	addr   string
	secret string

	m      *melody.Melody
	server *http.Server

	clients     map[string]*WechatClient
	clientsLock sync.RWMutex

	websocketRequests     map[int]chan<- *WebsocketCommand
	websocketRequestsLock sync.RWMutex
	wsRequestID           int32
}

var upgrader = websocket.Upgrader{}

// handleWsMessage - melody.HandleMessage
func (ws *WechatService) handleWsMessage(s *melody.Session, data []byte) {
	var msg WebsocketMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		ws.log.Warnln("Error reading from websocket:", err)
	}

	if msg.MXID != "" {
		s.Set(msg.MXID, struct{}{})
	}

	if msg.Command == "" {
		ws.clientsLock.RLock()
		client, ok := ws.clients[msg.MXID]
		if !ok {
			ws.log.Warnln("Dropping event to %d: no receiver", msg.MXID)
			return
		}
		go client.HandleEvent(&msg)
		ws.clientsLock.RUnlock()
	} else if msg.Command == CommandPing { // handle reconnected agent
		ws.log.Infofln("agent reconnected with mxid = %s", msg.MXID)
	} else if msg.Command == CommandResponse || msg.Command == CommandError { // handle matrix command response
		ws.websocketRequestsLock.RLock()
		respChan, ok := ws.websocketRequests[msg.ReqID]
		if ok {
			select {
			case respChan <- &msg.WebsocketCommand:
			default:
				ws.log.Warnfln("Failed to handle response to %d: channel didn't accept response", msg.ReqID)
			}
		} else {
			ws.log.Warnfln("Dropping response to %d: unknown request ID", msg.ReqID)
		}
		ws.websocketRequestsLock.RUnlock()
	} else {
		ws.log.Warnfln("Unknown Command: %s", msg.Command)
	}
}

// handleWsConnect - melody.HandleConnect
func (ws *WechatService) handleWsConnect(s *melody.Session) {
	ws.log.Infoln("WechatService websocket Connected")
}

// handleWsDisconnect - melody.HandleDisconnect
func (ws *WechatService) handleWsDisconnect(s *melody.Session) {
	ws.log.Infoln("WechatService websocket Disconnected")
}

func (ws *WechatService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Basic ") {
		errMissingToken.Write(w)
		return
	}

	if authHeader[len("Basic "):] != ws.secret {
		errUnknownToken.Write(w)
		return
	}

	ws.m.HandleRequest(w, r)
}

func (ws *WechatService) Start() {
	ws.log.Infoln("WechatService starting to listen on", ws.addr)
	err := ws.server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		ws.log.Fatalln("Error in listener:", err)
	}
}

func (ws *WechatService) Stop() {
	go ws.m.CloseWithMsg(
		melody.FormatCloseMessage(websocket.CloseGoingAway,
			fmt.Sprintf(`{"command": "%s", "status": "server_shutting_down"}`, CommandDisconnect)))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := ws.server.Shutdown(ctx)
	if err != nil {
		ws.log.Warnln("Failed to close server:", err)
	}
}

func (ws *WechatService) CreateClient(mxid string, handler func(*WebsocketMessage)) *WechatClient {
	ws.clientsLock.Lock()
	defer ws.clientsLock.Unlock()

	client := NewWechatClient(mxid, ws, handler)
	ws.clients[mxid] = client

	return client
}

func (ws *WechatService) RemoveClient(mxid string) {
	ws.clientsLock.Lock()
	defer ws.clientsLock.Unlock()

	delete(ws.clients, mxid)
}

func (ws *WechatService) RequestWebsocket(ctx context.Context, cmd *WebsocketRequest, response interface{}) error {
	cmd.ReqID = int(atomic.AddInt32(&ws.wsRequestID, 1))
	respChan := make(chan *WebsocketCommand, 1)

	ws.addWebsocketResponseWaiter(cmd.ReqID, respChan)
	defer ws.removeWebsocketResponseWaiter(cmd.ReqID, respChan)

	err := ws.SendWebsocket(cmd)
	if err != nil {
		return err
	}

	select {
	case resp := <-respChan:
		if resp.Command == CommandClosed {
			return ErrWebsocketClosed
		} else if resp.Command == CommandError {
			var respErr ErrorResponse
			err = json.Unmarshal(resp.Data, &respErr)
			if err != nil {
				return fmt.Errorf("failed to parse error JSON: %w", err)
			}
			return &respErr
		} else if response != nil {
			err = json.Unmarshal(resp.Data, &response)
			if err != nil {
				ws.log.Warnln(string(resp.Data))
				return fmt.Errorf("failed to parse response JSON: %w", err)
			}
			return nil
		} else {
			return nil
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (ws *WechatService) SendWebsocket(cmd *WebsocketRequest) error {
	jsonCmd, err := json.Marshal(cmd)
	if err != nil {
		return err
	}

	if cmd.Command == CommandIsLogin {
		return ws.m.Broadcast(jsonCmd)
	}

	sessions, err := ws.m.Sessions()
	if err != nil {
		ws.log.Warnfln("no websocket find")
		return ErrWebsocketNotConnected
	}

	var sess *melody.Session = nil
	switch cmd.Command {
	case CommandConnect:
		// TODO(xylonx): load balance
		if len(sessions) > 0 {
			sess = sessions[0]
		}
	default:
		for i := range sessions {
			if _, exists := sessions[i].Get(cmd.MXID); exists {
				sess = sessions[i]
			}
		}
	}

	if sess == nil {
		return fmt.Errorf("no session related to mxid: %s", cmd.MXID)
	}

	return sess.Write(jsonCmd)
}

func (ws *WechatService) addWebsocketResponseWaiter(reqID int, waiter chan<- *WebsocketCommand) {
	ws.websocketRequestsLock.Lock()
	ws.websocketRequests[reqID] = waiter
	ws.websocketRequestsLock.Unlock()
}

func (ws *WechatService) removeWebsocketResponseWaiter(reqID int, waiter chan<- *WebsocketCommand) {
	ws.websocketRequestsLock.Lock()
	existingWaiter, ok := ws.websocketRequests[reqID]
	if ok && existingWaiter == waiter {
		delete(ws.websocketRequests, reqID)
	}
	close(waiter)
	ws.websocketRequestsLock.Unlock()
}

func NewWechatService(addr, secret string, log log.Logger) *WechatService {
	service := &WechatService{
		log:               log,
		addr:              addr,
		secret:            secret,
		clients:           make(map[string]*WechatClient),
		websocketRequests: make(map[int]chan<- *WebsocketCommand),
	}
	service.m = melody.New()
	service.m.Config.MaxMessageSize = 10 * 1024 * 1024 // max message size is 10M
	service.m.Config.WriteWait = time.Minute * 3
	service.m.HandleConnect(service.handleWsConnect)
	service.m.HandleMessage(service.handleWsMessage)
	service.m.HandleDisconnect(service.handleWsDisconnect)
	service.server = &http.Server{
		Addr:    service.addr,
		Handler: service,
	}

	return service
}

// Error represents a Matrix protocol error.
type Error struct {
	HTTPStatus int       `json:"-"`
	ErrorCode  ErrorCode `json:"errcode"`
	Message    string    `json:"error"`
}

func (err Error) Write(w http.ResponseWriter) {
	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(err.HTTPStatus)
	_ = Respond(w, &err)
}

// ErrorCode is the machine-readable code in an Error.
type ErrorCode string

// Native ErrorCodes
const (
	ErrUnknownToken ErrorCode = "M_UNKNOWN_TOKEN"
	ErrBadJSON      ErrorCode = "M_BAD_JSON"
	ErrNotJSON      ErrorCode = "M_NOT_JSON"
	ErrUnknown      ErrorCode = "M_UNKNOWN"
)

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (er *ErrorResponse) Error() string {
	return fmt.Sprintf("%s: %s", er.Code, er.Message)
}

// Respond responds to a HTTP request with a JSON object.
func Respond(w http.ResponseWriter, data interface{}) error {
	w.Header().Add("Content-Type", "application/json")
	dataStr, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = w.Write(dataStr)
	return err
}
