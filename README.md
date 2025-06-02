

# Go Torrent Client

A simple BitTorrent client implemented in Go.  
This project parses `.torrent` files, communicates with trackers and peers, downloads pieces concurrently, and assembles the original file.

## Features

- Parses `.torrent` files using Bencode
- Connects to BitTorrent trackers to get peers
- Establishes peer connections with handshake and bitfield exchange
- Concurrent piece downloading with retries and integrity checks
- Limits maximum concurrent peer connections
- Saves the downloaded file locally

## Requirements

- Go 1.20+  
- Internet connection to access trackers and peers

## Usage

```bash
go run main.go <path-to-torrent-file> <output-file>
```

Example:

```bash
go run main.go ubuntu.torrent ubuntu.iso
```

## Project Structure

- `main.go`: Entry point, parses torrent and manages download
- `torrent.go`: Torrent metadata parsing and piece management
- `client.go`: Peer communication, handshake, messaging
- `download.go`: Piece downloading, integrity checking, concurrency control

## Notes

- This client currently supports basic peer protocol features.  
- Advanced features like DHT, peer exchange (PEX), or upload management are not yet implemented.  
- Use at your own risk.

## License

MIT License  
© 2025 Fabio Gualtieri

---