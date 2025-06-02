package main

import (
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackpal/bencode-go"
)

type BencodeInfo struct {
	Pieces      string `bencode:"pieces"`
	PieceLength int    `bencode:"piece length"`
	Length      int    `bencode:"length"`
	Name        string `bencode:"name"`
}

type BencodeTorrent struct {
	Announce string      `bencode:"announce"`
	Info     BencodeInfo `bencode:"info"`
}

type TorrentFile struct {
	Announce    string
	InfoHash    [20]byte
	PieceHashes [][20]byte
	PieceLength int
	Length      int
	Name        string
	Peers       []Peer
	PeerID      [20]byte
}

type Peer struct {
	IP   net.IP
	Port uint16
}

type PieceWork struct {
	Index  int
	Hash   [20]byte
	Length int
}

type PieceResult struct {
	Index int
	Buf   []byte
}

type messageID uint8

const (
	MsgChoke         messageID = 0
	MsgUnchoke       messageID = 1
	MsgInterested    messageID = 2
	MsgNotInterested messageID = 3
	MsgHave          messageID = 4
	MsgBitfield      messageID = 5
	MsgRequest       messageID = 6
	MsgPiece         messageID = 7
	MsgCancel        messageID = 8
)

type Message struct {
	ID      messageID
	Payload []byte
}

type Bitfield []byte

func (bf Bitfield) HasPiece(index int) bool {
	byteIndex := index / 8
	offset := index % 8
	return bf[byteIndex]>>(7-offset)&1 != 0
}

type Client struct {
	Conn     net.Conn
	Choked   bool
	Bitfield Bitfield
	Peer     Peer
}

func NewClient(peer Peer, peerID [20]byte, infoHash [20]byte) (*Client, error) {
	address := net.JoinHostPort(peer.IP.String(), strconv.Itoa(int(peer.Port)))
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		return nil, err
	}

	h := NewHandshake(infoHash, peerID)
	_, err = conn.Write(h.Serialize())
	if err != nil {
		return nil, err
	}

	handshakeResp := make([]byte, 68)
	_, err = io.ReadFull(conn, handshakeResp)
	if err != nil {
		return nil, err
	}

	msg, err := ReadMessage(conn)
	if err != nil {
		return nil, err
	}
	if msg.ID != MsgBitfield {
		return nil, errors.New("expected bitfield")
	}

	return &Client{
		Conn:     conn,
		Choked:   true,
		Bitfield: msg.Payload,
		Peer:     peer,
	}, nil
}

type Handshake struct {
	Pstr     string
	InfoHash [20]byte
	PeerID   [20]byte
}

func NewHandshake(infoHash, peerID [20]byte) *Handshake {
	return &Handshake{
		Pstr:     "BitTorrent protocol",
		InfoHash: infoHash,
		PeerID:   peerID,
	}
}

func (h *Handshake) Serialize() []byte {
	buf := make([]byte, 49+len(h.Pstr))
	buf[0] = byte(len(h.Pstr))
	curr := 1
	curr += copy(buf[curr:], h.Pstr)
	curr += copy(buf[curr:], make([]byte, 8))
	curr += copy(buf[curr:], h.InfoHash[:])
	curr += copy(buf[curr:], h.PeerID[:])
	return buf
}

func ReadMessage(r io.Reader) (*Message, error) {
	lenBuf := make([]byte, 4)
	_, err := io.ReadFull(r, lenBuf)
	if err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(lenBuf)
	if length == 0 {
		return nil, nil
	}
	payload := make([]byte, length)
	_, err = io.ReadFull(r, payload)
	if err != nil {
		return nil, err
	}
	return &Message{ID: messageID(payload[0]), Payload: payload[1:]}, nil
}

func (m *Message) Serialize() []byte {
	length := uint32(len(m.Payload) + 1)
	buf := make([]byte, 4+length)
	binary.BigEndian.PutUint32(buf[0:4], length)
	buf[4] = byte(m.ID)
	copy(buf[5:], m.Payload)
	return buf
}

func checkIntegrity(pw *PieceWork, buf []byte) error {
	hash := sha1.Sum(buf)
	if hash != pw.Hash {
		return fmt.Errorf("integrity check failed")
	}
	return nil
}

func generatePeerID() [20]byte {
	var peerID [20]byte
	s := fmt.Sprintf("GO0001-%x", rand.Uint32())
	copy(peerID[:], []byte(s))
	return peerID
}

func (t *TorrentFile) buildTrackerURL(peerID [20]byte, port uint16) (string, error) {
	base, err := url.Parse(t.Announce)
	if err != nil {
		return "", err
	}
	params := url.Values{
		"info_hash":  []string{string(t.InfoHash[:])},
		"peer_id":    []string{string(peerID[:])},
		"port":       []string{strconv.Itoa(int(port))},
		"uploaded":   []string{"0"},
		"downloaded": []string{"0"},
		"left":       []string{strconv.Itoa(t.Length)},
		"compact":    []string{"1"},
	}
	base.RawQuery = params.Encode()
	return base.String(), nil
}

func (t *TorrentFile) Download() ([]byte, error) {
	peerID := generatePeerID()
	t.PeerID = peerID

	workQueue := make(chan *PieceWork, len(t.PieceHashes))
	results := make(chan *PieceResult)
	peerSem := make(chan struct{}, 10) // Limit to 10 connections

	for index, hash := range t.PieceHashes {
		length := t.calculatePieceSize(index)
		workQueue <- &PieceWork{Index: index, Hash: hash, Length: length}
	}

	var wg sync.WaitGroup
	for _, peer := range t.Peers {
		peerSem <- struct{}{}
		wg.Add(1)
		go func(p Peer) {
			defer func() {
				<-peerSem
				wg.Done()
			}()
			client, err := NewClient(p, peerID, t.InfoHash)
			if err != nil {
				log.Println("failed client:", err)
				return
			}
			for pw := range workQueue {
				if !client.Bitfield.HasPiece(pw.Index) {
					workQueue <- pw
					continue
				}
				buf := make([]byte, pw.Length) // Dummy logic
				err := checkIntegrity(pw, buf)
				if err != nil {
					log.Println("retrying piece:", pw.Index)
					workQueue <- pw
					continue
				}
				results <- &PieceResult{Index: pw.Index, Buf: buf}
			}
		}(peer)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	buf := make([]byte, t.Length)
	done := 0
	for res := range results {
		begin := res.Index * t.PieceLength
		end := begin + len(res.Buf)
		copy(buf[begin:end], res.Buf)
		done++
		if done == len(t.PieceHashes) {
			break
		}
	}

	return buf, nil
}

func (t *TorrentFile) calculatePieceSize(index int) int {
	if index == len(t.PieceHashes)-1 {
		remainder := t.Length % t.PieceLength
		if remainder == 0 {
			return t.PieceLength
		}
		return remainder
	}
	return t.PieceLength
}

func main() {
	if len(os.Args) < 3 {
		log.Fatal("Usage: ./torrent <input.torrent> <output>")
	}
	file, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	torrent, err := bdecode(file)
	if err != nil {
		log.Fatal(err)
	}

	tf := torrent.toTorrentFile()
	data, err := tf.Download()
	if err != nil {
		log.Fatal(err)
	}
	os.WriteFile(os.Args[2], data, 0644)
}

func bdecode(r io.Reader) (*BencodeTorrent, error) {
	bto := &BencodeTorrent{}
	err := bencode.Unmarshal(r, bto)
	return bto, err
}

func (bto *BencodeTorrent) toTorrentFile() *TorrentFile {
	hash := sha1.Sum([]byte(bto.Info.Pieces)) // Simpler for example only
	pieceHashes := make([][20]byte, 0)
	for i := 0; i+20 <= len(bto.Info.Pieces); i += 20 {
		var h [20]byte
		copy(h[:], bto.Info.Pieces[i:i+20])
		pieceHashes = append(pieceHashes, h)
	}
	return &TorrentFile{
		Announce:    bto.Announce,
		InfoHash:    hash,
		PieceHashes: pieceHashes,
		PieceLength: bto.Info.PieceLength,
		Length:      bto.Info.Length,
		Name:        bto.Info.Name,
	}
}
