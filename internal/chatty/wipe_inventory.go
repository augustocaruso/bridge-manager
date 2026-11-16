package chatty

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type wipeIdentity struct {
	owner    string
	bot      string
	sender   string
	patterns []*regexp.Regexp
}

var matrixInventoryID = regexp.MustCompile(`^[@!][a-zA-Z0-9._=+~:$-]+$`)
var matrixHashRoomID = regexp.MustCompile(`^![a-zA-Z0-9_-]{43}$`)

func validInventoryID(value string, prefix byte) bool {
	if prefix == '!' && matrixHashRoomID.MatchString(value) {
		return true
	}
	return len(value) > 2 && value[0] == prefix && strings.Contains(value, ":") && matrixInventoryID.MatchString(value)
}

func (identity wipeIdentity) owns(user string) bool {
	if user == identity.bot || user == identity.sender {
		return true
	}
	for _, pattern := range identity.patterns {
		if pattern.MatchString(user) {
			return true
		}
	}
	return false
}

func (m *Manager) wipeIdentity(b Bridge) (wipeIdentity, error) {
	registration, err := m.registration(b)
	if err != nil {
		return wipeIdentity{}, err
	}
	config, err := readYAML(m.bridgePath(b, "config.native.yaml"))
	if err != nil {
		return wipeIdentity{}, err
	}
	bot, ok := mapAt(config, "appservice", "bot")["username"].(string)
	if !ok || bot == "" {
		return wipeIdentity{}, errors.New("bot missing from configuration")
	}
	identity := wipeIdentity{bot: "@" + bot + ":" + m.Config.ServerName, sender: "@" + registration["sender_localpart"].(string) + ":" + m.Config.ServerName}
	if !validInventoryID(identity.bot, '@') || !validInventoryID(identity.sender, '@') {
		return wipeIdentity{}, errors.New("invalid local bridge identity")
	}
	namespaces := registration["namespaces"].(map[string]any)
	users, ok := namespaces["users"].([]any)
	if !ok || len(users) == 0 {
		return wipeIdentity{}, errors.New("empty user namespace; inventory refused")
	}
	for _, value := range users {
		entry, ok := value.(map[string]any)
		if !ok || entry["exclusive"] != true {
			return wipeIdentity{}, errors.New("user namespace must be exclusive")
		}
		expression, ok := entry["regex"].(string)
		if !ok || expression == "" {
			return wipeIdentity{}, errors.New("namespace regex missing")
		}
		pattern, err := regexp.Compile(`\A(?:` + expression + `)\z`)
		if err != nil {
			return wipeIdentity{}, errors.New("invalid namespace regex")
		}
		identity.patterns = append(identity.patterns, pattern)
	}
	return identity, nil
}

type wipeUser struct {
	Name        string `json:"name"`
	Admin       bool   `json:"admin"`
	Deactivated bool   `json:"deactivated"`
	Erased      *bool  `json:"erased"`
}

type wipeRoom struct {
	ID   string
	Kind string
}
type wipePlan struct {
	Protected wipeIdentity
	Rooms     []wipeRoom
	Mixed     []string
	Kept      int
	Users     []wipeUser
}

func (m *Manager) wipeSession(ctx context.Context) (string, string, error) {
	data, err := readRegular(filepath.Join(m.Config.RuntimeRoot, "secrets/matrix-admin.json"))
	if err != nil {
		return "", "", errors.New("existing administrative session unavailable; no login will be performed")
	}
	var session struct {
		Token string `json:"access_token"`
		User  string `json:"user_id"`
	}
	if json.Unmarshal(data, &session) != nil || session.Token == "" || !validInventoryID(session.User, '@') || !strings.HasSuffix(session.User, ":"+m.Config.ServerName) {
		return "", "", errors.New("invalid existing administrative session")
	}
	var who struct {
		User string `json:"user_id"`
	}
	if err := m.request(ctx, http.MethodGet, "/_matrix/client/v3/account/whoami", session.Token, nil, &who); err != nil {
		return "", "", err
	}
	if who.User != session.User {
		return "", "", errors.New("session does not match the registered owner")
	}
	return session.Token, session.User, nil
}

func (m *Manager) wipeUsers(ctx context.Context, token string) ([]wipeUser, error) {
	users := []wipeUser{}
	seen := map[string]bool{}
	total := -1
	for offset := 0; ; {
		var page struct {
			Users []wipeUser `json:"users"`
			Total *int       `json:"total"`
			Next  *string    `json:"next_token"`
		}
		path := fmt.Sprintf("/_synapse/admin/v2/users?from=%d&limit=100&deactivated=true&locked=true", offset)
		if err := m.request(ctx, http.MethodGet, path, token, nil, &page); err != nil {
			return nil, err
		}
		if page.Total == nil || *page.Total < 0 || total >= 0 && total != *page.Total {
			return nil, errors.New("user total missing or changed; repeat the dry run")
		}
		total = *page.Total
		for _, user := range page.Users {
			if !validInventoryID(user.Name, '@') || seen[user.Name] {
				return nil, errors.New("invalid or duplicate user inventory")
			}
			seen[user.Name] = true
			users = append(users, user)
		}
		offset += len(page.Users)
		if page.Next == nil {
			if offset != total {
				return nil, errors.New("incomplete user inventory")
			}
			break
		}
		next, err := strconv.Atoi(*page.Next)
		if err != nil || len(page.Users) == 0 || next != offset || next >= total {
			return nil, errors.New("invalid user pagination")
		}
	}
	sort.Slice(users, func(left, right int) bool { return users[left].Name < users[right].Name })
	return users, nil
}

func (m *Manager) wipeRooms(ctx context.Context, token string) ([]string, error) {
	rooms := []string{}
	seen := map[string]bool{}
	total := -1
	for offset := 0; ; {
		var page struct {
			Rooms []struct {
				ID string `json:"room_id"`
			} `json:"rooms"`
			Total  *int `json:"total_rooms"`
			Offset *int `json:"offset"`
			Next   *int `json:"next_batch"`
		}
		path := fmt.Sprintf("/_synapse/admin/v1/rooms?from=%d&limit=100&order_by=name", offset)
		if err := m.request(ctx, http.MethodGet, path, token, nil, &page); err != nil {
			return nil, err
		}
		if page.Total == nil || *page.Total < 0 || page.Offset == nil || *page.Offset != offset || total >= 0 && total != *page.Total {
			return nil, errors.New("room inventory changed or invalid; repeat the dry run")
		}
		total = *page.Total
		for _, room := range page.Rooms {
			if !validInventoryID(room.ID, '!') || seen[room.ID] {
				return nil, errors.New("invalid or duplicate room ID")
			}
			seen[room.ID] = true
			rooms = append(rooms, room.ID)
		}
		offset += len(page.Rooms)
		if page.Next == nil {
			if offset != total {
				return nil, errors.New("incomplete room inventory")
			}
			break
		}
		if len(page.Rooms) == 0 || *page.Next != offset || *page.Next >= total {
			return nil, errors.New("invalid room pagination")
		}
	}
	sort.Strings(rooms)
	return rooms, nil
}

type wipeStateEvent struct {
	Type    string `json:"type"`
	Key     string `json:"state_key"`
	Content struct {
		Membership string `json:"membership"`
		Protocol   struct {
			ID string `json:"id"`
		} `json:"protocol"`
	} `json:"content"`
}

func (m *Manager) wipeRoomKind(ctx context.Context, b Bridge, identity, protected wipeIdentity, token, room string) (string, error) {
	associated, foreign, hasGhost, hasBot, hasOwner, marker := false, false, false, false, false, false
	for _, eventType := range []string{"m.room.member", "m.bridge", "uk.half-shot.bridge"} {
		var state struct {
			Events *[]wipeStateEvent `json:"state"`
		}
		path := "/_synapse/admin/v1/rooms/" + url.PathEscape(room) + "/state?type=" + url.QueryEscape(eventType)
		if err := m.request(ctx, http.MethodGet, path, token, nil, &state); err != nil {
			return "", err
		}
		if state.Events == nil {
			return "", errors.New("room state missing")
		}
		for _, event := range *state.Events {
			if event.Type != eventType {
				return "", errors.New("server did not respect the state filter")
			}
			if eventType != "m.room.member" {
				if event.Content.Protocol.ID == b.Name {
					marker = true
				} else {
					foreign = true
				}
				continue
			}
			membership := event.Content.Membership
			if membership != "join" && membership != "invite" && membership != "leave" && membership != "ban" && membership != "knock" {
				return "", errors.New("membership missing or invalid")
			}
			if !validInventoryID(event.Key, '@') {
				return "", errors.New("invalid member ID")
			}
			if membership != "join" && membership != "invite" {
				continue
			}
			if protected.owns(event.Key) {
				if identity.owns(event.Key) {
					return "", errors.New("namespace overlaps another bridge; wipe refused")
				}
				foreign = true
				continue
			}
			switch {
			case event.Key == identity.owner:
				hasOwner = true
			case event.Key == identity.bot || event.Key == identity.sender:
				hasBot = true
				associated = true
			case identity.owns(event.Key) && strings.HasSuffix(event.Key, ":"+m.Config.ServerName):
				hasGhost = true
				associated = true
			default:
				foreign = true
			}
		}
	}
	if foreign && (associated || marker) {
		return "mixed", nil
	}
	if foreign {
		return "kept", nil
	}
	if marker || hasGhost {
		return "portal", nil
	}
	if hasBot && hasOwner {
		return "bot", nil
	}
	if associated {
		return "mixed", nil
	}
	return "kept", nil
}

func (m *Manager) wipeInventory(ctx context.Context, b Bridge, identity wipeIdentity, token string) (wipePlan, error) {
	plan := wipePlan{}
	users, err := m.wipeUsers(ctx, token)
	if err != nil {
		return plan, err
	}
	otherName := "instagram"
	if b.Name == "instagram" {
		otherName = "whatsapp"
	}
	other, _ := Lookup(otherName)
	otherConfig, err := readYAML(m.bridgePath(other, "config.native.yaml"))
	if err != nil {
		return plan, err
	}
	otherRegistration, err := m.registration(other)
	if err != nil {
		return plan, err
	}
	otherBot, ok := mapAt(otherConfig, "appservice", "bot")["username"].(string)
	if !ok || otherBot == "" {
		return plan, errors.New("other bridge identity unavailable")
	}
	protected := wipeIdentity{bot: "@" + otherBot + ":" + m.Config.ServerName, sender: "@" + otherRegistration["sender_localpart"].(string) + ":" + m.Config.ServerName}
	otherNamespaces := otherRegistration["namespaces"].(map[string]any)
	otherUsers, ok := otherNamespaces["users"].([]any)
	if !ok || len(otherUsers) == 0 {
		return plan, errors.New("invalid other bridge namespace")
	}
	for _, value := range otherUsers {
		entry, ok := value.(map[string]any)
		if !ok {
			return plan, errors.New("invalid other bridge namespace")
		}
		expression, ok := entry["regex"].(string)
		if !ok || expression == "" {
			return plan, errors.New("invalid other bridge namespace")
		}
		pattern, err := regexp.Compile(`\A(?:` + expression + `)\z`)
		if err != nil {
			return plan, errors.New("invalid other bridge regex")
		}
		protected.patterns = append(protected.patterns, pattern)
	}
	plan.Protected = protected
	if identity.owns(protected.bot) || identity.owns(protected.sender) {
		return plan, errors.New("namespace includes another bridge identity; wipe refused")
	}
	for _, user := range users {
		if identity.owns(user.Name) {
			if protected.owns(user.Name) {
				return plan, errors.New("namespace overlaps another bridge; wipe refused")
			}
			if user.Admin || user.Name == identity.owner || user.Name == "@conduit:"+m.Config.ServerName || !strings.HasSuffix(user.Name, ":"+m.Config.ServerName) {
				return plan, errors.New("namespace includes protected user; wipe refused")
			}
			plan.Users = append(plan.Users, user)
		}
	}
	if identity.owns(identity.owner) || identity.owns("@conduit:"+m.Config.ServerName) {
		return plan, errors.New("namespace includes owner or system user")
	}
	rooms, err := m.wipeRooms(ctx, token)
	if err != nil {
		return plan, err
	}
	for _, room := range rooms {
		kind, err := m.wipeRoomKind(ctx, b, identity, protected, token, room)
		if err != nil {
			return plan, err
		}
		switch kind {
		case "portal", "bot":
			plan.Rooms = append(plan.Rooms, wipeRoom{room, kind})
		case "mixed":
			plan.Mixed = append(plan.Mixed, room)
		default:
			plan.Kept++
		}
	}
	return plan, nil
}
