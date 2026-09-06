package tls

import (
	"encoding/binary"
	"testing"
)

// buildClientHello constructs a syntactically valid TLS ClientHello record.
// If sniHostname is non-empty, a server_name extension is included.
// If includeEmptyExtensions is true (and sniHostname is empty), an empty
// extensions block is still written (extensions_len=0).
func buildClientHello(sniHostname string, includeEmptyExtensions bool) []byte {
	var body []byte
	body = append(body, 0x03, 0x03)          // client_version
	body = append(body, make([]byte, 32)...) // random
	body = append(body, 0x00)                // session_id_len = 0

	cipherSuites := []byte{0x00, 0x2f}
	body = append(body, byte(len(cipherSuites)>>8), byte(len(cipherSuites)))
	body = append(body, cipherSuites...)

	compression := []byte{0x00}
	body = append(body, byte(len(compression)))
	body = append(body, compression...)

	var extensions []byte
	if sniHostname != "" {
		var ext []byte
		nameLen := len(sniHostname)
		listLen := 1 + 2 + nameLen                         // name_type(1) + name_len(2) + name
		ext = append(ext, byte(listLen>>8), byte(listLen)) // server_name_list_length
		ext = append(ext, 0x00)                            // name_type = host_name
		ext = append(ext, byte(nameLen>>8), byte(nameLen))
		ext = append(ext, []byte(sniHostname)...)

		extensions = append(extensions, 0x00, 0x00) // extension_type = server_name
		extensions = append(extensions, byte(len(ext)>>8), byte(len(ext)))
		extensions = append(extensions, ext...)
	}
	if sniHostname != "" || includeEmptyExtensions {
		extLenBytes := make([]byte, 2)
		binary.BigEndian.PutUint16(extLenBytes, uint16(len(extensions)))
		body = append(body, extLenBytes...)
		body = append(body, extensions...)
	}

	var hs []byte
	hs = append(hs, 0x01) // msg_type = ClientHello
	hsLen := len(body)
	hs = append(hs, byte(hsLen>>16), byte(hsLen>>8), byte(hsLen))
	hs = append(hs, body...)

	var record []byte
	record = append(record, 0x16, 0x03, 0x03) // content_type, version
	recLen := len(hs)
	record = append(record, byte(recLen>>8), byte(recLen))
	record = append(record, hs...)
	return record
}

func TestModeFor(t *testing.T) {
	cases := map[string]Mode{
		"terminate":   ModeTerminate,
		"ktls":        ModeTerminate,
		"sni":         ModeSNI,
		"passthrough": ModePassthrough,
		"":            ModePassthrough,
		"bogus":       ModePassthrough,
	}
	for in, want := range cases {
		if got := ModeFor(in); got != want {
			t.Errorf("ModeFor(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsTLSClientHello(t *testing.T) {
	valid := buildClientHello("example.com", false)
	if !IsTLSClientHello(valid) {
		t.Fatal("expected a valid ClientHello to be recognized")
	}
	if IsTLSClientHello([]byte{0x16, 0x03}) {
		t.Fatal("expected too-short data to be rejected")
	}
	if IsTLSClientHello([]byte("GET / HTTP/1.1\r\n")) {
		t.Fatal("expected plaintext HTTP to be rejected")
	}
	notHandshake := append([]byte{0x17, 0x03, 0x03, 0x00, 0x00}, 0x01)
	if IsTLSClientHello(notHandshake) {
		t.Fatal("expected content_type 0x17 (application_data) to be rejected")
	}
}

func TestExtractSNIWithExtension(t *testing.T) {
	data := buildClientHello("example.com", false)
	host, err := ExtractSNI(data)
	if err != nil {
		t.Fatalf("ExtractSNI: unexpected error: %v", err)
	}
	if host != "example.com" {
		t.Fatalf("ExtractSNI: got %q, want %q", host, "example.com")
	}
}

func TestExtractSNILongerHostname(t *testing.T) {
	data := buildClientHello("api.omega-lb-controlplane.internal.example.org", false)
	host, err := ExtractSNI(data)
	if err != nil {
		t.Fatalf("ExtractSNI: unexpected error: %v", err)
	}
	if host != "api.omega-lb-controlplane.internal.example.org" {
		t.Fatalf("ExtractSNI: got %q", host)
	}
}

func TestExtractSNINoExtensionsPresent(t *testing.T) {
	data := buildClientHello("", false) // no extensions block at all
	_, err := ExtractSNI(data)
	if err != ErrNoSNI {
		t.Fatalf("expected ErrNoSNI when no extensions block is present, got %v", err)
	}
}

func TestExtractSNIEmptyExtensionsBlock(t *testing.T) {
	data := buildClientHello("", true) // extensions_len=0, no SNI
	_, err := ExtractSNI(data)
	if err != ErrNoSNI {
		t.Fatalf("expected ErrNoSNI for an empty extensions block, got %v", err)
	}
}

func TestExtractSNINotAClientHello(t *testing.T) {
	if _, err := ExtractSNI([]byte("GET / HTTP/1.1\r\nHost: example.com\r\n")); err != ErrNotClientHello {
		t.Fatalf("expected ErrNotClientHello for plaintext HTTP, got %v", err)
	}
	if _, err := ExtractSNI([]byte{0x16, 0x03}); err != ErrNotClientHello {
		t.Fatalf("expected ErrNotClientHello for too-short data, got %v", err)
	}
}

func TestExtractSNITruncatedRecord(t *testing.T) {
	data := buildClientHello("example.com", false)
	truncated := data[:len(data)-10] // record header claims more bytes than present
	if _, err := ExtractSNI(truncated); err == nil {
		t.Fatal("expected an error for a truncated TLS record")
	}
}
