package storage

import (
	"errors"
	"os"

	"github.com/khlieng/dispatch/pkg/session"
)

var (
	Path directory

	GetMessageStore          MessageStoreCreator
	GetMessageSearchProvider MessageSearchProviderCreator
)

func Initialize(root, dataRoot, configRoot string) {
	if root != DefaultDirectory() {
		Path.dataRoot = root
		Path.configRoot = root
	} else {
		Path.dataRoot = dataRoot
		Path.configRoot = configRoot
	}
	os.MkdirAll(Path.DataRoot(), 0700)
	os.MkdirAll(Path.ConfigRoot(), 0700)
}

var (
	ErrNotFound = errors.New("no item found")
)

type Store interface {
	GetUsers() ([]*User, error)
	SaveUser(user *User) error
	DeleteUser(user *User) error

	GetServer(user *User, host string) (*Server, error)
	GetServers(user *User) ([]*Server, error)
	SaveServer(user *User, server *Server) error
	RemoveServer(user *User, host string) error

	GetChannels(user *User) ([]*Channel, error)
	AddChannel(user *User, channel *Channel) error
	RemoveChannel(user *User, server, channel string) error

	GetOpenDMs(user *User) ([]Tab, error)
	AddOpenDM(user *User, server, nick string) error
	RemoveOpenDM(user *User, server, nick string) error
}

type SessionStore interface {
	GetSessions() ([]*session.Session, error)
	SaveSession(session *session.Session) error
	DeleteSession(key string) error
}

type MessageStore interface {
	LogMessage(message *Message) error
	LogMessages(messages []*Message) error
	GetMessages(server, channel string, count int, fromID string) ([]Message, bool, error)
	GetMessagesByID(server, channel string, ids []string) ([]Message, error)
	Close()
}

type MessageStoreCreator func(*User) (MessageStore, error)

type MessageSearchProvider interface {
	SearchMessages(server, channel, q string) ([]string, error)
	Index(id string, message *Message) error
	Close()
}

type MessageSearchProviderCreator func(*User) (MessageSearchProvider, error)
