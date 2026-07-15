package telegram

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	gotdmessages "github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"

	"github.com/ardasevinc/tele/internal/peerstore"
	"github.com/ardasevinc/tele/internal/privatefs"
)

type MediaDownloadOptions struct {
	Peer      string
	MessageID int
	OutDir    string
}

type MediaDownloadResult struct {
	OK           bool   `json:"ok"`
	PeerRef      string `json:"peer_ref"`
	MessageID    int    `json:"message_id"`
	Path         string `json:"path"`
	Bytes        int64  `json:"bytes"`
	MediaType    string `json:"media_type"`
	MimeType     string `json:"mime_type,omitempty"`
	FileName     string `json:"file_name"`
	StorageType  string `json:"storage_type,omitempty"`
	DownloadedAt string `json:"downloaded_at"`
}

func (a App) DownloadMedia(ctx context.Context, opts MediaDownloadOptions) (MediaDownloadResult, error) {
	if strings.TrimSpace(opts.Peer) == "" {
		return MediaDownloadResult{}, fmt.Errorf("peer is required")
	}
	if opts.MessageID <= 0 {
		return MediaDownloadResult{}, fmt.Errorf("msg-id must be positive")
	}
	var out MediaDownloadResult
	err := a.Run(ctx, func(ctx context.Context, c *telegram.Client) error {
		input, peerRef, err := a.resolvePeer(ctx, c, opts.Peer)
		if err != nil {
			return err
		}
		msg, err := fetchMessage(ctx, c, input, peerRef, opts.MessageID)
		if err != nil {
			return err
		}
		file, ok := (gotdmessages.Elem{Msg: msg, Peer: input}).File()
		if !ok {
			return fmt.Errorf("message %d has no downloadable media", opts.MessageID)
		}
		dir := strings.TrimSpace(opts.OutDir)
		if dir == "" {
			dir, err = os.MkdirTemp("", "tele-media-*")
			if err != nil {
				return err
			}
		}
		if err := privatefs.EnsureDir(dir); err != nil {
			return err
		}
		name := safeDownloadFileName(opts.MessageID, file.Name)
		path := filepath.Join(dir, name)
		storageType, err := downloadToPath(ctx, c, file.Location, path)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		peer := peerRef.Ref
		if peer == "" {
			peer = opts.Peer
		}
		out = MediaDownloadResult{
			OK:           true,
			PeerRef:      peer,
			MessageID:    opts.MessageID,
			Path:         path,
			Bytes:        info.Size(),
			MediaType:    mediaTypeName(msg),
			MimeType:     file.MIMEType,
			FileName:     name,
			DownloadedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if storageType != nil {
			out.StorageType = storageType.TypeName()
		}
		return nil
	})
	return out, err
}

func downloadToPath(ctx context.Context, c *telegram.Client, location tg.InputFileLocationClass, path string) (_ tg.StorageFileTypeClass, err error) {
	return atomicDownload(filepath.Clean(path), func(w io.WriterAt) (tg.StorageFileTypeClass, error) {
		return c.Download(location).Parallel(ctx, w)
	})
}

func atomicDownload(path string, download func(io.WriterAt) (tg.StorageFileTypeClass, error)) (storage tg.StorageFileTypeClass, err error) {
	err = privatefs.AtomicReplaceFile(path, func(file *os.File) error {
		var downloadErr error
		storage, downloadErr = download(file)
		return downloadErr
	})
	return storage, err
}

func fetchMessage(ctx context.Context, c *telegram.Client, input tg.InputPeerClass, peerRef peerstore.Peer, msgID int) (*tg.Message, error) {
	id := []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}}
	var (
		res tg.MessagesMessagesClass
		err error
	)
	if channel, ok := inputChannel(input, peerRef); ok {
		res, err = c.API().ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: channel, ID: id})
	} else {
		res, err = c.API().MessagesGetMessages(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	for _, cls := range messageClasses(res) {
		msg, ok := cls.(*tg.Message)
		if ok && msg.ID == msgID {
			return msg, nil
		}
	}
	return nil, fmt.Errorf("message %d not found", msgID)
}

func inputChannel(input tg.InputPeerClass, peerRef peerstore.Peer) (*tg.InputChannel, bool) {
	if channel, ok := input.(*tg.InputPeerChannel); ok {
		return &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}, true
	}
	if peerRef.Kind == "channel" || peerRef.Kind == "supergroup" {
		return &tg.InputChannel{ChannelID: peerRef.ID, AccessHash: peerRef.AccessHash}, true
	}
	return nil, false
}

func mediaTypeName(msg *tg.Message) string {
	if msg == nil {
		return ""
	}
	media, ok := msg.GetMedia()
	if !ok || media == nil {
		return ""
	}
	return media.TypeName()
}

func safeDownloadFileName(msgID int, name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "media"
	}
	name = strings.Map(func(r rune) rune {
		switch r {
		case 0, '/', '\\', ':':
			return '-'
		default:
			return r
		}
	}, name)
	return fmt.Sprintf("%d-%s", msgID, name)
}
