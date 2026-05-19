// Package tls provides TLS inspection utilities for Omega-LB.
//
// ─── THE TLS TERMINATION TRAP ────────────────────────────────────────────────
// Omega-LB's route_manager.bpf.c does path-based routing by reading plaintext
// URL bytes from request_ctx.path.  This works perfectly for HTTP/1.1 and
// cleartext HTTP/2.  It silently breaks for TLS traffic.
//
// There are three topologies, and only one of them works transparently:
//
//   Mode           Who holds cert     eBPF sees           Path routing
//   ─────────────  ─────────────      ────────────────     ─────────────
//   terminate      LB (kTLS)          plaintext bytes      works ✓
//   sni            LB (no cert)       TLS ClientHello      SNI only ✓
//   passthrough    backend            encrypted payload    BROKEN ✗
//
// ─── MODE: terminate (kTLS) ──────────────────────────────────────────────────
// The LB terminates TLS using kTLS (kernel TLS, BPF_SKB_PROG_TYPE).
// After TLS handshake, the kernel decrypts the TCP stream transparently before
// handing bytes to eBPF programs.  route_manager sees plaintext HTTP bytes and
// does full L7 path-based routing.
// Requirements: Linux 4.13+, CONFIG_TLS=y, certificate/key on the LB node.
// Setup: configure cert_file + key_file in omega-lb.yaml; the daemon sets
// TCP_ULP=tls on the accepted socket before handing to the eBPF chain.
//
// ─── MODE: sni (SNI routing) ─────────────────────────────────────────────────
// The LB does NOT terminate TLS but reads the SNI hostname from the TLS
// ClientHello handshake record (which IS plaintext — SNI is sent before
// encryption begins).  filter_manager extracts the SNI and writes it into
// request_ctx.path so route_manager can match on hostname instead of URL path.
// Only L4 cluster routing is possible in this mode (by hostname, not path).
// This file implements the Go-side SNI parser used for userspace tests and
// the CLI snidump tool.  The eBPF-side parser lives in filter_manager.bpf.c.
//
// ─── MODE: passthrough ───────────────────────────────────────────────────────
// The LB does NOT inspect TLS at all.  route_manager receives PROTO_TLS_PASSTHROUGH
// and routes all traffic to a single "default TLS cluster" without path matching.
// All rules that depend on URL paths are silently bypassed — this is the trap.
// Use this mode only when you need end-to-end TLS and cannot touch the certificate.
//
// ─── TLS ClientHello Wire Format ─────────────────────────────────────────────
//
//   TLS record:
//     [0]    content_type  = 0x16 (handshake)
//     [1..2] version       = 0x0301 (TLS 1.0 compat) or 0x0303
//     [3..4] length        (uint16 big-endian)
//
//   Handshake header:
//     [5]    msg_type      = 0x01 (ClientHello)
//     [6..8] length        (uint24 big-endian)
//
//   ClientHello body:
//     [9..10]  client_version
//     [11..42] random (32 bytes)
//     [43]     session_id_len
//     [43+1 .. 43+session_id_len] session_id
//     ...cipher_suites, compression_methods...
//     extensions:
//       [N..N+1] extensions_length
//       foreach extension:
//         [+0..+1] extension_type   (0x0000 = server_name)
//         [+2..+3] extension_data_length
//         if type == 0x0000:
//           [+4..+5] server_name_list_length
//           [+6]     name_type (0x00 = host_name)
//           [+7..+8] name_length
//           [+9..]   name bytes (UTF-8 ASCII hostname)
//
package tls

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Mode describes how the load balancer handles TLS traffic.
type Mode int

const (
	// ModePassthrough: LB passes TLS bytes to backend unchanged.
	// path-based routing is impossible; only cluster-level routing applies.
	ModePassthrough Mode = iota
	// ModeTerminate: LB terminates TLS using kTLS (kernel TLS).
	// Full L7 path routing available after decryption.
	ModeTerminate
	// ModeSNI: LB reads SNI from ClientHello for cluster selection.
	// No cert needed; only hostname-level routing (no URL path access).
	ModeSNI
)

// ModeFor maps the config string to a Mode constant.
// Unknown strings default to ModePassthrough (safest).
func ModeFor(s string) Mode {
	switch s {
	case "terminate", "ktls":
		return ModeTerminate
	case "sni":
		return ModeSNI
	default:
		return ModePassthrough
	}
}

// ErrNotClientHello is returned when the data does not begin with a TLS
// ClientHello record.
var ErrNotClientHello = errors.New("tls: not a ClientHello record")

// ErrNoSNI is returned when the ClientHello is valid but contains no SNI
// extension.
var ErrNoSNI = errors.New("tls: no SNI extension in ClientHello")

// ExtractSNI parses a TLS ClientHello record and returns the server_name
// (SNI) extension value.
//
// data should be the first bytes of a TCP connection (at least 5 bytes for
// the TLS record header; 300 bytes typical for a full ClientHello).
//
// Returns (hostname, nil) on success.
// Returns ("", ErrNotClientHello) if data is not a TLS handshake record.
// Returns ("", ErrNoSNI) if no server_name extension is present.
// Returns ("", err) for any other parse error.
func ExtractSNI(data []byte) (string, error) {
	// Minimum: TLS record header (5 bytes) + handshake type (1 byte)
	if len(data) < 6 {
		return "", ErrNotClientHello
	}

	// TLS record content type must be 0x16 (handshake)
	if data[0] != 0x16 {
		return "", ErrNotClientHello
	}

	// TLS version: must be 0x03xx (SSL 3.0 / TLS 1.x)
	if data[1] != 0x03 {
		return "", ErrNotClientHello
	}

	recordLen := int(binary.BigEndian.Uint16(data[3:5]))
	if len(data) < 5+recordLen {
		return "", fmt.Errorf("tls: truncated record: want %d, have %d", 5+recordLen, len(data))
	}

	// Handshake header starts at offset 5
	if data[5] != 0x01 { // msg_type ClientHello
		return "", ErrNotClientHello
	}

	// Handshake length (3 bytes big-endian)
	if len(data) < 9 {
		return "", ErrNotClientHello
	}
	hsLen := int(data[6])<<16 | int(data[7])<<8 | int(data[8])
	if len(data) < 9+hsLen {
		return "", fmt.Errorf("tls: truncated handshake: want %d, have %d", 9+hsLen, len(data))
	}

	// ClientHello body starts at offset 9
	// Layout: version(2) + random(32) + session_id_len(1) + session_id(N) +
	//         cipher_suites_len(2) + cipher_suites(N) +
	//         compression_len(1) + compression(N) +
	//         extensions_len(2) + extensions(...)
	pos := 9 // start of ClientHello body

	// Skip client_version (2 bytes)
	pos += 2
	// Skip random (32 bytes)
	pos += 32
	if pos >= len(data) {
		return "", ErrNotClientHello
	}

	// Skip session_id
	sessionIDLen := int(data[pos])
	pos += 1 + sessionIDLen
	if pos+2 > len(data) {
		return "", ErrNotClientHello
	}

	// Skip cipher_suites
	cipherSuitesLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2 + cipherSuitesLen
	if pos+1 > len(data) {
		return "", ErrNotClientHello
	}

	// Skip compression_methods
	compressionLen := int(data[pos])
	pos += 1 + compressionLen
	if pos+2 > len(data) {
		return "", ErrNoSNI // no extensions present
	}

	// Extensions
	extensionsLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	end := pos + extensionsLen
	if end > len(data) {
		end = len(data)
	}

	for pos+4 <= end {
		extType := binary.BigEndian.Uint16(data[pos : pos+2])
		extLen := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
		pos += 4

		if extType == 0x0000 { // server_name extension
			// server_name_list_length (2 bytes)
			if pos+2 > end {
				break
			}
			listLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
			pos += 2
			listEnd := pos + listLen
			if listEnd > end {
				listEnd = end
			}
			for pos+3 <= listEnd {
				nameType := data[pos]
				nameLen := int(binary.BigEndian.Uint16(data[pos+1 : pos+3]))
				pos += 3
				if pos+nameLen > listEnd {
					break
				}
				if nameType == 0x00 { // host_name
					return string(data[pos : pos+nameLen]), nil
				}
				pos += nameLen
			}
			return "", ErrNoSNI
		}
		pos += extLen
	}

	return "", ErrNoSNI
}

// IsTLSClientHello reports whether data begins with a TLS ClientHello record.
// This is a quick check (no full parse) used by filter_manager to classify
// new connections.
func IsTLSClientHello(data []byte) bool {
	if len(data) < 6 {
		return false
	}
	return data[0] == 0x16 && data[1] == 0x03 && data[5] == 0x01
}
