package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/go-faster/errors"
	bolt "go.etcd.io/bbolt"
	bolterrors "go.etcd.io/bbolt/errors"

	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
)

// Bucket names. The server runs a single account, so user IDs are not part of
// the keys.
var (
	// accessHashBucket holds channel access hashes, keyed by channel ID.
	accessHashBucket = []byte("access_hash")
	// dialogsBucket holds the persisted dialog cache, keyed by channel ID.
	dialogsBucket = []byte("dialogs")
	// messagesBucket holds per-channel sub-buckets of buffered unread messages.
	messagesBucket = []byte("messages")
)

// openStateDB opens (creating if needed) the bbolt database used to persist the
// updates state (pts/qts/date/seq) and channel access hashes, so that the
// updates manager can recover via getDifference across restarts instead of
// re-syncing from scratch.
func openStateDB(cfg Config) (*bolt.DB, error) {
	path := filepath.Join(cfg.SessionDir, "updates.bolt")

	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, errors.Wrapf(err, "open %s", path)
	}

	return db, nil
}

// accessHasher is an updates.ChannelAccessHasher backed by bbolt.
type accessHasher struct {
	db *bolt.DB
}

var _ updates.ChannelAccessHasher = accessHasher{}

func (h accessHasher) SetChannelAccessHash(_ context.Context, _, channelID, accessHash int64) error {
	return h.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(accessHashBucket)
		if err != nil {
			return errors.Wrap(err, "create bucket")
		}

		return b.Put(i64b(channelID), i64b(accessHash))
	})
}

func (h accessHasher) GetChannelAccessHash(_ context.Context, _, channelID int64) (int64, bool, error) {
	var (
		hash  int64
		found bool
	)
	err := h.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(accessHashBucket)
		if b == nil {
			return nil
		}
		v := b.Get(i64b(channelID))
		if v == nil {
			return nil
		}
		hash = b2i64(v)
		found = true

		return nil
	})
	if err != nil {
		return 0, false, errors.Wrap(err, "get access hash")
	}

	return hash, found, nil
}

// dialogStore persists the dialog cache to bbolt so that the dialog list does
// not need to be re-fetched on every start. On restart the persisted cache is
// kept current by the updates manager via getDifference.
type dialogStore struct {
	db *bolt.DB
}

func dialogKeyParts(kind string, id int64) string {
	return fmt.Sprintf("%s:%d", kind, id)
}

func dialogKey(ch UnreadChannel) string {
	switch ch.peer.(type) {
	case *tg.InputPeerUser:
		return dialogKeyParts("user", ch.ID)
	case *tg.InputPeerChat:
		return dialogKeyParts("chat", ch.ID)
	default:
		return dialogKeyParts("channel", ch.ID)
	}
}

func dialogStoreKey(ch UnreadChannel) []byte {
	return []byte(dialogKey(ch))
}

// storedDialog is the on-disk representation of an UnreadChannel. The access
// hash is stored explicitly so the input peer can be rebuilt on load.
type storedDialog struct {
	ID             int64  `json:"id"`
	Title          string `json:"title"`
	Username       string `json:"username,omitempty"`
	Type           string `json:"type,omitempty"`
	UnreadCount    int    `json:"unread_count"`
	UnreadMark     bool   `json:"unread_mark,omitempty"`
	Broadcast      bool   `json:"broadcast,omitempty"`
	Megagroup      bool   `json:"megagroup,omitempty"`
	ReadInboxMaxID int    `json:"read_inbox_max_id"`
	AccessHash     int64  `json:"access_hash"`
	// IsUser and IsChat distinguish peer kinds for non-channel dialogs.
	// Channels use Broadcast/Megagroup.
	IsUser bool `json:"is_user,omitempty"`
	IsChat bool `json:"is_chat,omitempty"`
}

func toStored(ch UnreadChannel) storedDialog {
	var accessHash int64
	isUser, isChat := false, false
	typ := ch.Type
	switch p := ch.peer.(type) {
	case *tg.InputPeerChannel:
		accessHash = p.AccessHash
		if typ == "" {
			typ = typeChannel
		}
	case *tg.InputPeerUser:
		accessHash = p.AccessHash
		isUser = true
		if typ == "" {
			typ = typePrivate
		}
	case *tg.InputPeerChat:
		isChat = true
		if typ == "" {
			typ = typeGroup
		}
	}

	return storedDialog{
		ID:             ch.ID,
		Title:          ch.Title,
		Username:       ch.Username,
		Type:           typ,
		UnreadCount:    ch.UnreadCount,
		UnreadMark:     ch.UnreadMark,
		Broadcast:      typ == typeChannel,
		Megagroup:      typ == typeSupergroup,
		ReadInboxMaxID: ch.readInboxMaxID,
		AccessHash:     accessHash,
		IsUser:         isUser,
		IsChat:         isChat,
	}
}

func (s storedDialog) toChannel() UnreadChannel {
	var peer tg.InputPeerClass
	typ := s.Type
	switch {
	case s.IsUser:
		peer = &tg.InputPeerUser{UserID: s.ID, AccessHash: s.AccessHash}
		if typ == "" {
			typ = typePrivate
		}
	case s.IsChat:
		peer = &tg.InputPeerChat{ChatID: s.ID}
		if typ == "" {
			typ = typeGroup
		}
	default:
		peer = &tg.InputPeerChannel{ChannelID: s.ID, AccessHash: s.AccessHash}
		if typ == "" {
			// Written before Type existed: the bools are all there is.
			typ = typeChannel
			if s.Megagroup {
				typ = typeSupergroup
			}
		}
	}

	return UnreadChannel{
		ID:             s.ID,
		Title:          s.Title,
		Username:       s.Username,
		UnreadCount:    s.UnreadCount,
		UnreadMark:     s.UnreadMark,
		Type:           typ,
		readInboxMaxID: s.ReadInboxMaxID,
		peer:           peer,
	}
}

// put upserts a single dialog.
func (d *dialogStore) put(ch UnreadChannel) error {
	data, err := json.Marshal(toStored(ch))
	if err != nil {
		return errors.Wrap(err, "marshal dialog")
	}

	return d.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(dialogsBucket)
		if err != nil {
			return errors.Wrap(err, "create bucket")
		}

		return b.Put(dialogStoreKey(ch), data)
	})
}

// putAll replaces the whole persisted dialog set, dropping dialogs that are no
// longer present.
func (d *dialogStore) putAll(chs []UnreadChannel) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(dialogsBucket); err != nil && !errors.Is(err, bolterrors.ErrBucketNotFound) {
			return errors.Wrap(err, "delete bucket")
		}
		b, err := tx.CreateBucket(dialogsBucket)
		if err != nil {
			return errors.Wrap(err, "create bucket")
		}

		for _, ch := range chs {
			data, err := json.Marshal(toStored(ch))
			if err != nil {
				return errors.Wrap(err, "marshal dialog")
			}
			if err := b.Put(dialogStoreKey(ch), data); err != nil {
				return errors.Wrap(err, "put dialog")
			}
		}

		return nil
	})
}

// delete removes a single dialog by channel ID.
func (d *dialogStore) delete(channelID int64) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(dialogsBucket)
		if b == nil {
			return nil
		}

		for _, kind := range []string{"channel", "chat", "user"} {
			if err := b.Delete([]byte(dialogKeyParts(kind, channelID))); err != nil {
				return err
			}
		}

		return b.Delete(i64b(channelID))
	})
}

// load returns all persisted dialogs.
func (d *dialogStore) load() ([]UnreadChannel, error) {
	var out []UnreadChannel
	err := d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(dialogsBucket)
		if b == nil {
			return nil
		}

		return b.ForEach(func(_, v []byte) error {
			var sd storedDialog
			if err := json.Unmarshal(v, &sd); err != nil {
				return errors.Wrap(err, "unmarshal dialog")
			}
			out = append(out, sd.toChannel())

			return nil
		})
	})
	if err != nil {
		return nil, errors.Wrap(err, "load dialogs")
	}

	return out, nil
}

// messageStore buffers recent unread messages per channel so that
// read_channel_unread can be served without a messages.getHistory RPC. Messages
// arrive for free on the update stream (updateNewChannelMessage, and the same
// after getDifference); this mirrors tdlib, which serves history from its local
// store and only hits the server to fill gaps.
//
// Layout: messagesBucket -> per-channel sub-bucket keyed by i64b(channelID) ->
// message ID (big-endian, so keys sort by ID) -> JSON Message.
type messageStore struct {
	db  *bolt.DB
	cap int // max buffered messages per channel; older ones are trimmed.
}

// msgKey encodes a message ID as a big-endian key so bbolt iterates in ID order.
func msgKey(id int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(id))

	return b
}

// channelBucket returns the per-channel message sub-bucket, creating it (and the
// root) when create is set. Returns nil when it does not exist and create is
// false.
func channelBucket(tx *bolt.Tx, channelID int64, create bool) (*bolt.Bucket, error) {
	if !create {
		root := tx.Bucket(messagesBucket)
		if root == nil {
			return nil, nil
		}

		return root.Bucket(i64b(channelID)), nil
	}

	root, err := tx.CreateBucketIfNotExists(messagesBucket)
	if err != nil {
		return nil, errors.Wrap(err, "root bucket")
	}
	b, err := root.CreateBucketIfNotExists(i64b(channelID))
	if err != nil {
		return nil, errors.Wrap(err, "channel bucket")
	}

	return b, nil
}

// append buffers an incoming message, trimming the oldest beyond the cap.
func (s *messageStore) append(channelID int64, m Message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return errors.Wrap(err, "marshal message")
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := channelBucket(tx, channelID, true)
		if err != nil {
			return err
		}
		if err := b.Put(msgKey(m.ID), data); err != nil {
			return errors.Wrap(err, "put message")
		}

		return trimOldest(b, s.cap)
	})
}

// edit overwrites a buffered message in place. Messages that are not buffered
// (already read/pruned, or never seen) are ignored.
func (s *messageStore) edit(channelID int64, m Message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return errors.Wrap(err, "marshal message")
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := channelBucket(tx, channelID, false)
		if err != nil || b == nil {
			return err
		}
		if b.Get(msgKey(m.ID)) == nil {
			return nil
		}
		if err := b.Put(msgKey(m.ID), data); err != nil {
			return errors.Wrap(err, "put message")
		}

		return nil
	})
}

// deleteMessages drops buffered messages by ID.
func (s *messageStore) deleteMessages(channelID int64, ids []int) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := channelBucket(tx, channelID, false)
		if err != nil || b == nil {
			return err
		}
		for _, id := range ids {
			if err := b.Delete(msgKey(id)); err != nil {
				return errors.Wrap(err, "delete message")
			}
		}

		return nil
	})
}

// pruneRead drops buffered messages that are now read (ID <= maxID).
func (s *messageStore) pruneRead(channelID int64, maxID int) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := channelBucket(tx, channelID, false)
		if err != nil || b == nil {
			return err
		}

		max := msgKey(maxID)
		var keys [][]byte
		c := b.Cursor()
		for k, _ := c.First(); k != nil && bytes.Compare(k, max) <= 0; k, _ = c.Next() {
			keys = append(keys, append([]byte(nil), k...))
		}
		for _, k := range keys {
			if err := b.Delete(k); err != nil {
				return errors.Wrap(err, "prune message")
			}
		}

		return nil
	})
}

// deleteChannel drops the whole message buffer of a channel.
func (s *messageStore) deleteChannel(channelID int64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket(messagesBucket)
		if root == nil {
			return nil
		}
		if err := root.DeleteBucket(i64b(channelID)); err != nil && !errors.Is(err, bolterrors.ErrBucketNotFound) {
			return errors.Wrap(err, "delete channel messages")
		}

		return nil
	})
}

// load returns buffered unread messages (ID > afterID), newest first. A positive
// limit caps the result; limit <= 0 returns all buffered unread messages.
func (s *messageStore) load(channelID int64, afterID, limit int) ([]Message, error) {
	var out []Message
	err := s.db.View(func(tx *bolt.Tx) error {
		b, err := channelBucket(tx, channelID, false)
		if err != nil || b == nil {
			return err
		}

		after := msgKey(afterID)
		c := b.Cursor()
		for k, v := c.Last(); k != nil && bytes.Compare(k, after) > 0; k, v = c.Prev() {
			var m Message
			if err := json.Unmarshal(v, &m); err != nil {
				return errors.Wrap(err, "unmarshal message")
			}
			out = append(out, m)
			if limit > 0 && len(out) >= limit {
				break
			}
		}

		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "load messages")
	}

	return out, nil
}

// trimOldest deletes the lowest-ID (oldest) messages until at most max remain.
func trimOldest(b *bolt.Bucket, max int) error {
	var keys [][]byte
	c := b.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		keys = append(keys, append([]byte(nil), k...))
	}
	if len(keys) <= max {
		return nil
	}
	for _, k := range keys[:len(keys)-max] {
		if err := b.Delete(k); err != nil {
			return errors.Wrap(err, "trim message")
		}
	}

	return nil
}

func i64b(v int64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(v))

	return b
}

func b2i64(b []byte) int64 {
	return int64(binary.LittleEndian.Uint64(b))
}
