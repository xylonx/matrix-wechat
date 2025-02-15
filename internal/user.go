package internal

import (
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/duo/matrix-wechat/internal/database"
	"github.com/duo/matrix-wechat/internal/types"
	"github.com/duo/matrix-wechat/internal/wechat"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	log "maunium.net/go/maulogger/v2"
)

const (
	resyncMinInterval  = 7 * 24 * time.Hour
	resyncLoopInterval = 4 * time.Hour
)

var (
	ErrAlreadyLoggedIn = errors.New("already logged in")
)

type resyncQueueItem struct {
	portal *Portal
	puppet *Puppet
}

type User struct {
	*database.User

	Client *wechat.WechatClient

	bridge *WechatBridge
	log    log.Logger

	Admin           bool
	Whitelisted     bool
	PermissionLevel bridgeconfig.PermissionLevel

	mgmtCreateLock  sync.Mutex
	spaceCreateLock sync.Mutex
	connLock        sync.Mutex

	spaceMembershipChecked bool

	BridgeState *bridge.BridgeStateQueue

	resyncQueue     map[types.UID]resyncQueueItem
	resyncQueueLock sync.Mutex
	nextResync      time.Time
}

func (u *User) GetPermissionLevel() bridgeconfig.PermissionLevel {
	return u.PermissionLevel
}

func (u *User) GetManagementRoomID() id.RoomID {
	return u.ManagementRoom
}

func (u *User) GetMXID() id.UserID {
	return u.MXID
}

func (u *User) GetCommandState() map[string]interface{} {
	return nil
}

func (u *User) addToUIDMap() {
	u.bridge.usersLock.Lock()
	u.bridge.usersByUsername[u.UID.Uin] = u
	u.bridge.usersLock.Unlock()
}

func (u *User) removeFromUIDMap(state status.BridgeState) {
	u.bridge.usersLock.Lock()
	uidUser, ok := u.bridge.usersByUsername[u.UID.Uin]
	if ok && u == uidUser {
		delete(u.bridge.usersByUsername, u.UID.Uin)
	}
	u.bridge.usersLock.Unlock()
	u.BridgeState.Send(state)
}
func (u *User) puppetResyncLoop() {
	u.nextResync = time.Now().Add(resyncLoopInterval).Add(-time.Duration(rand.Intn(3600)) * time.Second)
	for {
		time.Sleep(time.Until(u.nextResync))
		u.nextResync = time.Now().Add(resyncLoopInterval)
		u.doPuppetResync()
	}
}

func (u *User) EnqueuePuppetResync(puppet *Puppet) {
	if puppet.LastSync.Add(resyncMinInterval).After(time.Now()) {
		return
	}
	u.resyncQueueLock.Lock()
	if _, exists := u.resyncQueue[puppet.UID]; !exists {
		u.resyncQueue[puppet.UID] = resyncQueueItem{puppet: puppet}
		u.log.Debugfln("Enqueued resync for %s (next sync in %s)", puppet.UID, time.Until(u.nextResync))
	}
	u.resyncQueueLock.Unlock()
}

func (u *User) EnqueuePortalResync(portal *Portal) {
	if !portal.IsGroupChat() || portal.LastSync.Add(resyncMinInterval).After(time.Now()) {
		return
	}
	u.resyncQueueLock.Lock()
	if _, exists := u.resyncQueue[portal.Key.UID]; !exists {
		u.resyncQueue[portal.Key.UID] = resyncQueueItem{portal: portal}
		u.log.Debugfln("Enqueued resync for %s (next sync in %s)", portal.Key.UID, time.Until(u.nextResync))
	}
	u.resyncQueueLock.Unlock()
}

func (u *User) doPuppetResync() {
	if !u.IsLoggedIn() {
		return
	}
	u.resyncQueueLock.Lock()
	if len(u.resyncQueue) == 0 {
		u.resyncQueueLock.Unlock()
		return
	}
	queue := u.resyncQueue
	u.resyncQueue = make(map[types.UID]resyncQueueItem)
	u.resyncQueueLock.Unlock()
	var puppets []*Puppet
	var portals []*Portal
	for uid, item := range queue {
		var lastSync time.Time
		if item.puppet != nil {
			lastSync = item.puppet.LastSync
		} else if item.portal != nil {
			lastSync = item.portal.LastSync
		}
		if lastSync.Add(resyncMinInterval).After(time.Now()) {
			u.log.Debugfln("Not resyncing %s, last sync was %s ago", uid, time.Until(lastSync))
			continue
		}
		if item.puppet != nil {
			puppets = append(puppets, item.puppet)
		} else if item.portal != nil {
			portals = append(portals, item.portal)
		}
	}
	for _, portal := range portals {
		groupInfo := u.Client.GetGroupInfo(portal.Key.Receiver.Uin)
		if groupInfo != nil {
			m := u.Client.GetGroupMembers(portal.Key.Receiver.Uin)
			if m == nil {
				u.log.Warnfln("Failed to get group members for %s to do background sync", portal.Key.UID)
			} else {
				groupInfo.Members = m
				u.log.Debugfln("Doing background sync for %s", portal.Key.UID)
				portal.UpdateMatrixRoom(u, groupInfo, false)
			}
		} else {
			u.log.Warnfln("Failed to get group info for %s to do background sync", portal.Key.UID)
		}
	}
	for _, puppet := range puppets {
		u.log.Debugfln("Doing background sync for user: %v", puppet.UID)
		info := u.Client.GetUserInfo(puppet.UID.Uin)
		if info != nil {
			puppet.Sync(u, types.NewContact(info.ID, info.Nickname, info.Remark), true, true)
		} else {
			u.log.Warnfln("Failed to get contact info for %s in background sync", puppet.UID)
		}
	}
}

func (u *User) ensureInvited(intent *appservice.IntentAPI, roomID id.RoomID, isDirect bool) (ok bool) {
	extraContent := make(map[string]interface{})
	if isDirect {
		extraContent["is_direct"] = true
	}
	customPuppet := u.bridge.GetPuppetByCustomMXID(u.MXID)
	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		extraContent["me.lxduo.wechat.will_auto_accept"] = true
	}
	_, err := intent.InviteUser(roomID, &mautrix.ReqInviteUser{UserID: u.MXID}, extraContent)
	var httpErr mautrix.HTTPError
	if err != nil && errors.As(err, &httpErr) && httpErr.RespError != nil && strings.Contains(httpErr.RespError.Err, "is already in the room") {
		u.bridge.StateStore.SetMembership(roomID, u.MXID, event.MembershipJoin)
		ok = true
		return
	} else if err != nil {
		u.log.Warnfln("Failed to invite user to %s: %v", roomID, err)
	} else {
		ok = true
	}

	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		err = customPuppet.CustomIntent().EnsureJoined(roomID, appservice.EnsureJoinedParams{IgnoreCache: true})
		if err != nil {
			u.log.Warnfln("Failed to auto-join %s: %v", roomID, err)
			ok = false
		} else {
			ok = true
		}
	}
	return
}

func (u *User) GetSpaceRoom() id.RoomID {
	if !u.bridge.Config.Bridge.PersonalFilteringSpaces {
		return ""
	}

	if len(u.SpaceRoom) == 0 {
		u.spaceCreateLock.Lock()
		defer u.spaceCreateLock.Unlock()
		if len(u.SpaceRoom) > 0 {
			return u.SpaceRoom
		}

		resp, err := u.bridge.Bot.CreateRoom(&mautrix.ReqCreateRoom{
			Visibility: "private",
			Name:       "WeChat",
			Topic:      "Your WeChat bridged chats",
			InitialState: []*event.Event{{
				Type: event.StateRoomAvatar,
				Content: event.Content{
					Parsed: &event.RoomAvatarEventContent{
						URL: u.bridge.Config.AppService.Bot.ParsedAvatar,
					},
				},
			}},
			CreationContent: map[string]interface{}{
				"type": event.RoomTypeSpace,
			},
			PowerLevelOverride: &event.PowerLevelsEventContent{
				Users: map[id.UserID]int{
					u.bridge.Bot.UserID: 9001,
					u.MXID:              50,
				},
			},
		})

		if err != nil {
			u.log.Errorln("Failed to auto-create space room:", err)
		} else {
			u.SpaceRoom = resp.RoomID
			u.Update()
			u.ensureInvited(u.bridge.Bot, u.SpaceRoom, false)
		}
	} else if !u.spaceMembershipChecked && !u.bridge.StateStore.IsInRoom(u.SpaceRoom, u.MXID) {
		u.ensureInvited(u.bridge.Bot, u.SpaceRoom, false)
	}
	u.spaceMembershipChecked = true

	return u.SpaceRoom
}

func (u *User) GetManagementRoom() id.RoomID {
	if len(u.ManagementRoom) == 0 {
		u.mgmtCreateLock.Lock()
		defer u.mgmtCreateLock.Unlock()

		if len(u.ManagementRoom) > 0 {
			return u.ManagementRoom
		}
		creationContent := make(map[string]interface{})
		if !u.bridge.Config.Bridge.FederateRooms {
			creationContent["m.federate"] = false
		}
		resp, err := u.bridge.Bot.CreateRoom(&mautrix.ReqCreateRoom{
			Topic:           "WeChat bridge notices",
			IsDirect:        true,
			CreationContent: creationContent,
		})
		if err != nil {
			u.log.Errorln("Failed to auto-create management room:", err)
		} else {
			u.SetManagementRoom(resp.RoomID)
		}
	}

	return u.ManagementRoom
}

func (u *User) SetManagementRoom(roomID id.RoomID) {
	existingUser, ok := u.bridge.managementRooms[roomID]
	if ok {
		existingUser.ManagementRoom = ""
		existingUser.Update()
	}

	u.ManagementRoom = roomID
	u.bridge.managementRooms[u.ManagementRoom] = u
	u.Update()
}

func (u *User) failedConnect(err error) {
	u.log.Warnln("Error connecting to WeChat:", err)
	u.Client.Disconnect()
	u.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateUnknownError,
		Error:      WechatConnectionFailed,
		Info: map[string]interface{}{
			"go_error": err.Error(),
		},
	})
	u.BridgeState.Send(status.BridgeState{StateEvent: status.StateUnknownError, Error: WechatConnectionFailed})
}

func (u *User) Login() error {
	u.connLock.Lock()
	defer u.connLock.Unlock()

	if u.IsLoggedIn() {
		return ErrAlreadyLoggedIn
	} else if u.Client != nil {
		u.unlockedDeleteConnection()
	}

	u.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting, Error: WechatConnecting})
	u.Client = u.bridge.WechatService.CreateClient(string(u.MXID), u.handleMessage)

	err := u.Client.Login()
	if err != nil {
		u.failedConnect(err)
	}

	return err
}

func (u *User) LoginWithQRCode() []byte {
	return u.Client.LoginWithQRCode()
}

func (u *User) MarkLogin() {
	info := u.Client.GetSelf()
	if info != nil {
		u.UID = types.NewUserUID(info.ID)
		u.addToUIDMap()
		u.Update()

		go u.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
		go u.tryAutomaticDoublePuppeting()

		u.log.Debugln("Login to wechat", u.UID)
	} else {
		u.log.Warnln("Failed to get self info.")
	}
}

func (u *User) unlockedDeleteConnection() {
	if u.Client == nil {
		return
	}
	u.Client.Disconnect()
	u.Client = nil
}

func (u *User) DeleteConnection() {
	u.connLock.Lock()
	defer u.connLock.Unlock()
	u.unlockedDeleteConnection()
}

func (u *User) DeleteSession() {
	if !u.UID.IsEmpty() {
		u.UID = types.EmptyUID
		u.Update()
	}
}

func (u *User) IsLoggedIn() bool {
	return u.Client != nil && u.Client.IsLoggedIn()
}

func (u *User) tryAutomaticDoublePuppeting() {
	if !u.bridge.Config.CanAutoDoublePuppet(u.MXID) {
		return
	}
	u.log.Debugln("Checking if double puppeting needs to be enabled")
	puppet := u.bridge.GetPuppetByUID(u.UID)
	if len(puppet.CustomMXID) > 0 {
		u.log.Debugln("User already has double-puppeting enabled")
		// Custom puppet already enabled
		return
	}
	accessToken, err := puppet.loginWithSharedSecret(u.MXID)
	if err != nil {
		u.log.Warnln("Failed to login with shared secret:", err)
		return
	}
	err = puppet.SwitchCustomMXID(accessToken, u.MXID)
	if err != nil {
		puppet.log.Warnln("Failed to switch to auto-logined custom puppet:", err)
		return
	}
	u.log.Infoln("Successfully automatically enabled custom puppet")
}

func (u *User) getDirectChats() map[id.UserID][]id.RoomID {
	res := make(map[id.UserID][]id.RoomID)
	privateChats := u.bridge.DB.Portal.FindPrivateChats(u.UID)
	for _, portal := range privateChats {
		if len(portal.MXID) > 0 {
			res[u.bridge.FormatPuppetMXID(portal.Key.UID)] = []id.RoomID{portal.MXID}
		}
	}

	return res
}

func (u *User) UpdateDirectChats(chats map[id.UserID][]id.RoomID) {
	if !u.bridge.Config.Bridge.SyncDirectChatList {
		return
	}
	puppet := u.bridge.GetPuppetByCustomMXID(u.MXID)
	if puppet == nil || puppet.CustomIntent() == nil {
		return
	}
	intent := puppet.CustomIntent()
	method := http.MethodPatch
	if chats == nil {
		chats = u.getDirectChats()
		method = http.MethodPut
	}
	u.log.Debugln("Updating m.direct list on homeserver")
	var err error
	existingChats := make(map[id.UserID][]id.RoomID)
	err = intent.GetAccountData(event.AccountDataDirectChats.Type, &existingChats)
	if err != nil {
		u.log.Warnln("Failed to get m.direct list to update it:", err)
		return
	}
	for userID, rooms := range existingChats {
		if _, ok := u.bridge.ParsePuppetMXID(userID); !ok {
			// This is not a ghost user, include it in the new list
			chats[userID] = rooms
		} else if _, ok := chats[userID]; !ok && method == http.MethodPatch {
			// This is a ghost user, but we're not replacing the whole list, so include it too
			chats[userID] = rooms
		}
	}
	err = intent.SetAccountData(event.AccountDataDirectChats.Type, &chats)
	if err != nil {
		u.log.Warnln("Failed to update m.direct list:", err)
	}
}

func (u *User) GetPortalByUID(uid types.UID) *Portal {
	return u.bridge.GetPortalByUID(database.NewPortalKey(uid, u.UID))
}

func (u *User) ResyncContacts(forceAvatarSync bool) error {
	for _, contact := range u.Client.GetFriendList() {
		uid := types.NewUserUID(contact.ID)
		puppet := u.bridge.GetPuppetByUID(uid)
		if puppet != nil {
			puppet.Sync(u, types.NewContact(contact.ID, contact.Nickname, contact.Remark), forceAvatarSync, true)
		} else {
			u.log.Warnfln("Got a nil puppet for %s while syncing contacts", uid)
		}
	}

	return nil
}

func (u *User) ResyncGroups(createPortals bool) error {
	for _, group := range u.Client.GetGroupList() {
		uid := types.NewGroupUID(group.ID)
		portal := u.GetPortalByUID(uid)
		if len(portal.MXID) == 0 {
			if createPortals {
				if err := portal.CreateMatrixRoom(u, group, true); err != nil {
					return fmt.Errorf("failed to create room for %s: %v", uid, err)
				}
			}
		} else {
			portal.UpdateMatrixRoom(u, group, true)
		}
	}

	return nil
}

func (u *User) StartPM(uid types.UID, reason string) (*Portal, *Puppet, bool, error) {
	u.log.Debugln("Starting PM with", uid, "from", reason)
	puppet := u.bridge.GetPuppetByUID(uid)
	puppet.SyncContact(u, true, reason)
	portal := u.GetPortalByUID(puppet.UID)
	if len(portal.MXID) > 0 {
		ok := portal.ensureUserInvited(u)
		if !ok {
			portal.log.Warnfln("ensureUserInvited(%s) returned false, creating new portal", u.MXID)
			portal.MXID = ""
		} else {
			return portal, puppet, false, nil
		}
	}
	err := portal.CreateMatrixRoom(u, nil, false)

	return portal, puppet, true, err
}

func (u *User) updateAvatar(uid types.UID, avatarID *string, avatarURL *id.ContentURI, avatarSet *bool, log log.Logger, intent *appservice.IntentAPI) bool {
	var url string
	if uid.IsUser() {
		if info := u.Client.GetUserInfo(uid.Uin); info != nil {
			url = info.BigAvatar
		}
	} else {
		if info := u.Client.GetGroupInfo(uid.Uin); info != nil {
			url = info.BigAvatar
		}
	}

	if len(url) == 0 || url == *avatarID {
		return false
	}

	resp, err := reuploadAvatar(intent, url)
	if err != nil {
		u.log.Warnln("Failed to reupload avatar:", err)
		return false
	}

	*avatarURL = resp
	*avatarID = url
	*avatarSet = false

	return true
}

func (u *User) handleMessage(m *wechat.WebsocketMessage) {
	if strings.HasSuffix(m.Target, "@chatroom") { // Group
		uid := types.NewGroupUID(m.Target)
		portal := u.bridge.GetPortalByUID(database.NewPortalKey(uid, u.UID))
		portal.messages <- PortalMessage{event: m, source: u}
	} else {
		var key database.PortalKey
		if m.Sender == u.UID.Uin {
			key = database.NewPortalKey(types.NewUserUID(m.Target), types.NewUserUID(m.Sender))
		} else {
			key = database.NewPortalKey(types.NewUserUID(m.Sender), types.NewUserUID(m.Target))
		}
		portal := u.bridge.GetPortalByUID(key)
		portal.messages <- PortalMessage{event: m, source: u}
	}
}

// ChildOverride
func (br *WechatBridge) GetIUser(userID id.UserID, create bool) bridge.User {
	return br.getUserByMXID(userID, false)
}

func (br *WechatBridge) GetUserByMXID(userID id.UserID) *User {
	return br.getUserByMXID(userID, false)
}

func (br *WechatBridge) getUserByMXID(userID id.UserID, onlyIfExists bool) *User {
	_, isPuppet := br.ParsePuppetMXID(userID)
	if isPuppet || userID == br.Bot.UserID {
		return nil
	}

	br.usersLock.Lock()
	defer br.usersLock.Unlock()

	user, ok := br.usersByMXID[userID]
	if !ok {
		userIDPtr := &userID
		if onlyIfExists {
			userIDPtr = nil
		}

		return br.loadDBUser(br.DB.User.GetByMXID(userID), userIDPtr)
	}

	return user
}

func (br *WechatBridge) GetUserByMXIDIfExists(userID id.UserID) *User {
	return br.getUserByMXID(userID, true)
}

func (br *WechatBridge) GetUserByUID(uid types.UID) *User {
	br.usersLock.Lock()
	defer br.usersLock.Unlock()

	user, ok := br.usersByUsername[uid.Uin]
	if !ok {
		return br.loadDBUser(br.DB.User.GetByUin(uid.Uin), nil)
	}

	return user
}

func (br *WechatBridge) GetAllUsers() []*User {
	br.usersLock.Lock()
	defer br.usersLock.Unlock()

	dbUsers := br.DB.User.GetAll()
	output := make([]*User, len(dbUsers))
	for index, dbUser := range dbUsers {
		user, ok := br.usersByMXID[dbUser.MXID]
		if !ok {
			user = br.loadDBUser(dbUser, nil)
		}
		output[index] = user
	}

	return output
}

func (br *WechatBridge) loadDBUser(dbUser *database.User, mxid *id.UserID) *User {
	if dbUser == nil {
		if mxid == nil {
			return nil
		}
		dbUser = br.DB.User.New()
		dbUser.MXID = *mxid
		dbUser.Insert()
	}
	user := br.NewUser(dbUser)
	br.usersByMXID[user.MXID] = user
	if len(user.ManagementRoom) > 0 {
		br.managementRooms[user.ManagementRoom] = user
	}

	return user
}

func (br *WechatBridge) NewUser(dbUser *database.User) *User {
	user := &User{
		User:   dbUser,
		bridge: br,
		log:    br.Log.Sub("User").Sub(string(dbUser.MXID)),

		resyncQueue: make(map[types.UID]resyncQueueItem),
	}

	user.PermissionLevel = user.bridge.Config.Bridge.Permissions.Get(user.MXID)
	user.Whitelisted = user.PermissionLevel >= bridgeconfig.PermissionLevelUser
	user.Admin = user.PermissionLevel >= bridgeconfig.PermissionLevelAdmin
	user.BridgeState = br.NewBridgeStateQueue(user, user.log)

	go user.puppetResyncLoop()

	return user
}
